import yaml
import asyncio
import aiohttp
import time
import sys
import json
import os
from prometheus_client.parser import text_fd_to_metric_families
from io import StringIO

PROMSKETCH_BASE_PORT = 7100
MACHINES_PER_PORT = 200          # samakan dengan server
PROMSKETCH_CONTROL_URL = "http://localhost:7000"
PORT_BLOCKLIST = {7000}          # <— NEW: jangan pernah kirim ke 7000

total_sent = 0
start_time = time.time()

metrics_per_target = 1250
BATCH_SEND_INTERVAL_SECONDS = 0.5
POST_TIMEOUT_SECONDS = 8         # <— NEW: timeout kirim lebih lega
REGISTER_SLEEP_SECONDS = 0.5     # <— NEW: beri jeda setelah register

#  Ingester memetakan machineid → port memakai range ukuran MACHINES_PER_PORT:
def machine_to_port(machineid: str) -> int:
    # machineid format: "machine_0", "machine_1", ..., "machine_199"
    try:
        idx = int(str(machineid).split("_")[1])
    except Exception:
        idx = 0
    port_index = idx // MACHINES_PER_PORT
    return PROMSKETCH_BASE_PORT + port_index

# ingester baca num_samples_config.yml, hitung estimasi total time-series, lalu mendaftar ke main server :7000/register_config
async def register_capacity(config_data):
    targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
    num_targets = len(targets)
    total_ts_est = num_targets * metrics_per_target
    payload = {
        "num_targets": num_targets,
        "estimated_timeseries": total_ts_est,
        "machines_per_port": MACHINES_PER_PORT,
        "start_port": PROMSKETCH_BASE_PORT,
    }
    async with aiohttp.ClientSession() as session:
        try:
            async with session.post(f"{PROMSKETCH_CONTROL_URL}/register_config",
                                    json=payload, timeout=5) as resp:
                print("[REGISTER_CONFIG]", resp.status)
        except Exception as e:
            print("[REGISTER_CONFIG ERROR]", e)

# Ingester membaca daftar targets dari num_samples_config.yml, lalu HTTP GET ke http://<target>/metrics dan parse format teks Prometheus menjadi list sample
async def fetch_metrics(session, target):
    metrics = []
    try:
        async with session.get(f"http://{target}/metrics", timeout=5) as response:
            if response.status != 200:
                print(f"[ERROR] Failed to scrape {target}: {response.status}")
                return metrics
            text = await response.text()
            for family in text_fd_to_metric_families(StringIO(text)):
                for sample in family.samples:
                    labels_dict = dict(sample.labels)
                    metrics.append({
                        "Name": sample.name,
                        "Labels": labels_dict,
                        "Value": float(sample.value),
                    })
    except Exception as e:
        print(f"[ERROR] Scraping {target} failed: {e}")
    return metrics

async def log_speed():
    global total_sent
    while True:
        elapsed = time.time() - start_time
        if elapsed > 0:
            print(f"[INGEST SPEED] Sent {total_sent} samples in {elapsed:.2f}s "
                  f"= {total_sent/elapsed:.2f} samples/sec")
        await asyncio.sleep(5)

def parse_duration(duration_str):
    if duration_str.endswith('ms'):
        return int(duration_str[:-2]) / 1000
    if duration_str.endswith('s'):
        return int(duration_str[:-1])
    if duration_str.endswith('m'):
        return int(duration_str[:-1]) * 60
    if duration_str.endswith('h'):
        return int(duration_str[:-1]) * 3600
    raise ValueError(f"Unsupported duration format: {duration_str}")

async def post_with_retry(session, url, payload, retries=2):
    last_err = None
    for attempt in range(retries + 1):
        try:
            async with session.post(url, json=payload, timeout=POST_TIMEOUT_SECONDS) as resp:
                text = await resp.text()
                return resp.status, text
        except Exception as e:
            last_err = e
            await asyncio.sleep(0.2 * (attempt + 1))
    raise last_err

# ... kode sebelum ini tetap ...

async def ingest_loop(config_file):
    global total_sent

    try:
        with open(config_file, "r") as f:
            config_data = yaml.safe_load(f)
        await register_capacity(config_data)
        await asyncio.sleep(REGISTER_SLEEP_SECONDS)  # <— NEW: beri waktu port 71xx start
    except Exception as e:
        print(f"Error loading config file: {e}")
        sys.exit(1)

    targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
    num_targets = len(targets)
    # MAX_BATCH_SIZE = num_targets * metrics_per_target

    print(f"[CONFIG] metrics_per_target = {metrics_per_target}")
    # print(f"[CONFIG] MAX_BATCH_SIZE = {MAX_BATCH_SIZE}")
    print(f"[ROUTING] BASE_PORT={PROMSKETCH_BASE_PORT} MACHINES_PER_PORT={MACHINES_PER_PORT}")

    scrape_interval_str = config_data["scrape_configs"][0].get("scrape_interval", "10s")
    try:
        interval_seconds = parse_duration(scrape_interval_str)
        if interval_seconds <= 0:
            interval_seconds = 1
    except Exception as e:
        print(f"Invalid scrape_interval: {e}")
        interval_seconds = 1

    asyncio.create_task(log_speed())

    async with aiohttp.ClientSession() as session:
        while True:
            current_scrape_time = int(time.time() * 1000)
            tasks = [fetch_metrics(session, target) for target in targets]
            results = await asyncio.gather(*tasks)

            metrics_buffer = []
            for metric_list in results:
                metrics_buffer.extend(metric_list)

            # Langsung kirim semua metrics hasil fetch setiap interval
            if metrics_buffer:
                buckets = {}
                for m in metrics_buffer:
                    mid = m["Labels"].get("machineid", "machine_0")
                    port = machine_to_port(mid)
                    if port in PORT_BLOCKLIST:
                        port = PROMSKETCH_BASE_PORT
                    buckets.setdefault(port, []).append(m)

                for port, items in sorted(buckets.items()):
                    url = f"http://localhost:{port}/ingest"
                    payload = {"Timestamp": current_scrape_time, "Metrics": items}
                    try:
                        status, body = await post_with_retry(session, url, payload, retries=2)
                        if status == 200:
                            total_sent += len(items)
                            print(f"[SEND OK] {len(items)} → {url}")
                        else:
                            print(f"[SEND ERR {status}] {url} → {body[:200]}")
                    except Exception as e:
                        print(f"[SEND EXC] {url}: {e}")

            await asyncio.sleep(interval_seconds)

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=str, required=True, help="Path to Prometheus config file")
    args = parser.parse_args()
    asyncio.run(ingest_loop(args.config))
