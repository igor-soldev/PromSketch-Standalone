import yaml
import asyncio
import aiohttp
import time
import sys
import json
import threading
import os
from prometheus_client.parser import text_fd_to_metric_families
from io import StringIO

PROMSKETCH_GO_URL = "http://localhost:7000"
PROMSKETCH_INGEST_ENDPOINT = f"{PROMSKETCH_GO_URL}/ingest"

total_sent = 0
start_time = time.time()

metrics_per_target = 1250
BATCH_SEND_INTERVAL_SECONDS = 0.5

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
                    labels_dict = {k: v for k, v in sample.labels.items()}
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
            print(f"[INGEST SPEED] Sent {total_sent} samples in {elapsed:.2f}s = {total_sent/elapsed:.2f} samples/sec")
        await asyncio.sleep(5)

def parse_duration(duration_str):
    if duration_str.endswith('ms'):
        return int(duration_str[:-2]) / 1000
    elif duration_str.endswith('s'):
        return int(duration_str[:-1])
    elif duration_str.endswith('m'):
        return int(duration_str[:-1]) * 60
    elif duration_str.endswith('h'):
        return int(duration_str[:-1]) * 3600
    else:
        raise ValueError(f"Unsupported duration format: {duration_str}")

async def ingest_loop(config_file):
    global total_sent

    try:
        with open(config_file, "r") as f:
            config_data = yaml.safe_load(f)
    except Exception as e:
        print(f"Error loading config file: {e}")
        sys.exit(1)

    targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
    num_targets = len(targets)
    MAX_BATCH_SIZE = num_targets * metrics_per_target

    print(f"[CONFIG] metrics_per_target = {metrics_per_target}")
    print(f"[CONFIG] MAX_BATCH_SIZE = {MAX_BATCH_SIZE}")
    print(f"[CONFIG] BATCH_SEND_INTERVAL_SECONDS = {BATCH_SEND_INTERVAL_SECONDS}")

    scrape_interval_str = config_data["scrape_configs"][0].get("scrape_interval", "10s")
    try:
        interval_seconds = parse_duration(scrape_interval_str)
        if interval_seconds <= 0:
            interval_seconds = 1
    except Exception as e:
        print(f"Invalid scrape_interval: {e}")
        interval_seconds = 10

    metrics_buffer = []
    last_send_time = time.time()

    asyncio.create_task(log_speed())

    async with aiohttp.ClientSession() as session:
        while True:
            current_scrape_time = int(time.time() * 1000)
            tasks = [fetch_metrics(session, target) for target in targets]
            results = await asyncio.gather(*tasks)

            for metric_list in results:
                metrics_buffer.extend(metric_list)
                
            # If you want to log the number of metrics scraped per target, uncomment the following lines:
            # for i, metric_list in enumerate(results):
            #     target = targets[i]
            #     print(f"[SCRAPE] {target}: scraped {len(metric_list)} metrics")
            #     metrics_buffer.extend(metric_list)

            if len(metrics_buffer) >= MAX_BATCH_SIZE:
                if metrics_buffer:
                    payload = {
                        "Timestamp": current_scrape_time,
                        "Metrics": metrics_buffer,
                    }
                    try:
                        async with session.post(PROMSKETCH_INGEST_ENDPOINT, json=payload, timeout=5) as resp:
                            if resp.status == 200:
                                print(f"Sent {len(metrics_buffer)} metrics to server. Status: {resp.status}")
                                total_sent += len(metrics_buffer)
                            else:
                                print(f"[ERROR] Server responded with status: {resp.status}")
                    except Exception as e:
                        print(f"[ERROR] Sending to Go server: {e}")
                    metrics_buffer = []
                    last_send_time = time.time()

            await asyncio.sleep(interval_seconds)

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=str, required=True, help="Path to Prometheus config file")
    args = parser.parse_args()
    asyncio.run(ingest_loop(args.config))
