# import yaml
# import requests
# import time
# import sys
# import json
# import threading
# from prometheus_client.parser import text_fd_to_metric_families
# from io import StringIO
# from concurrent.futures import ThreadPoolExecutor

# # Configuration for your PromSketch Go Server
# PROMSKETCH_GO_URL = "http://localhost:7000"
# PROMSKETCH_INGEST_ENDPOINT = f"{PROMSKETCH_GO_URL}/ingest"

# # Global counter for speed logging
# total_sent = 0
# start_time = time.time()

# def parse_duration(duration_str):
#     if duration_str.endswith('ms'):
#         return int(duration_str[:-2]) / 1000
#     elif duration_str.endswith('s'):
#         return int(duration_str[:-1])
#     elif duration_str.endswith('m'):
#         return int(duration_str[:-1]) * 60
#     elif duration_str.endswith('h'):
#         return int(duration_str[:-1]) * 3600
#     else:
#         raise ValueError(f"Unsupported duration format: {duration_str}")

# def scrape_target(target):
#     metrics = []
#     try:
#         response = requests.get(f"http://{target}/metrics", timeout=2)
#         response.raise_for_status()
#         for family in text_fd_to_metric_families(StringIO(response.text)):
#             for sample in family.samples:
#                 labels_dict = {k: v for k, v in sample.labels.items()}
#                 metrics.append({
#                     "Name": sample.name,
#                     "Labels": labels_dict,
#                     "Value": float(sample.value),
#                 })
#     except Exception as e:
#         print(f"[ERROR] Failed to scrape {target}: {e}")
#     return metrics

# def log_speed():
#     while True:
#         elapsed = time.time() - start_time
#         if elapsed > 0:
#             print(f"[INGEST SPEED] Sent {total_sent} samples in {elapsed:.2f}s = {total_sent/elapsed:.2f} samples/sec")
#         time.sleep(5)

# def ingest_data(config_file):
#     global total_sent

#     try:
#         with open(config_file, "r") as f:
#             config_data = yaml.safe_load(f)
#     except Exception as e:
#         print(f"Error loading config file: {e}")
#         sys.exit(1)

#     targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
#     scrape_interval_str = config_data["scrape_configs"][0].get("scrape_interval", "10s")

#     try:
#         interval_seconds = parse_duration(scrape_interval_str)
#         if interval_seconds <= 0:
#             interval_seconds = 1
#     except Exception as e:
#         print(f"Invalid scrape_interval: {e}")
#         interval_seconds = 10

#     print(f"Starting ingestion from {len(targets)} targets every {interval_seconds}s")

#     ### 🔧 DYNAMIC PARAMETER TUNING BASED ON TARGETS ###
#     metrics_per_target = 1250  # adjust if each exporter uses different config
#     total_expected_metrics = len(targets) * metrics_per_target

#     # Adjust batch size and send interval dynamically
#     MAX_BATCH_SIZE = total_expected_metrics
#     RATE_FACTOR = 40000  # determines how aggressive we send (tune this if needed)
#     BATCH_SEND_INTERVAL_SECONDS = round(total_expected_metrics / RATE_FACTOR, 2)
#     if BATCH_SEND_INTERVAL_SECONDS < 0.2:
#         BATCH_SEND_INTERVAL_SECONDS = 0.2  # minimum delay

#     print(f"[CONFIG] Auto-adjusted MAX_BATCH_SIZE = {MAX_BATCH_SIZE}")
#     print(f"[CONFIG] Auto-adjusted BATCH_SEND_INTERVAL_SECONDS = {BATCH_SEND_INTERVAL_SECONDS}")

#     metrics_buffer = []
#     last_send_time = time.time()

#     threading.Thread(target=log_speed, daemon=True).start()

#     while True:
#         current_scrape_time = int(time.time() * 1000)

#         with ThreadPoolExecutor(max_workers=len(targets)) as executor:
#             results = executor.map(scrape_target, targets)

#         for metric_list in results:
#             metrics_buffer.extend(metric_list)

#         # Send if buffer is full or time is up
#         if (time.time() - last_send_time >= BATCH_SEND_INTERVAL_SECONDS) or (len(metrics_buffer) >= MAX_BATCH_SIZE):
#             if metrics_buffer:
#                 payload = {
#                     "Timestamp": current_scrape_time,
#                     "Metrics": metrics_buffer,
#                 }
#                 try:
#                     resp = requests.post(PROMSKETCH_INGEST_ENDPOINT, json=payload, timeout=5)
#                     resp.raise_for_status()
#                     print(f"✅ Sent {len(metrics_buffer)} metrics to server. Status: {resp.status_code}")
#                     total_sent += len(metrics_buffer)
#                 except Exception as e:
#                     print(f"[ERROR] Sending to Go server: {e}")
#                 metrics_buffer = []
#                 last_send_time = time.time()

#         time.sleep(interval_seconds)

# if __name__ == "__main__":
#     import argparse
#     parser = argparse.ArgumentParser()
#     parser.add_argument("--config", type=str, required=True, help="Path to Prometheus config file")
#     args = parser.parse_args()
#     ingest_data(args.config)

import yaml
import requests
import time
import sys
import json
import threading
import os
import re
from prometheus_client.parser import text_fd_to_metric_families
from io import StringIO
from concurrent.futures import ThreadPoolExecutor

PROMSKETCH_GO_URL = "http://localhost:7000"
PROMSKETCH_INGEST_ENDPOINT = f"{PROMSKETCH_GO_URL}/ingest"

total_sent = 0
start_time = time.time()

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

def scrape_target(target):
    metrics = []
    try:
        response = requests.get(f"http://{target}/metrics", timeout=2)
        response.raise_for_status()
        for family in text_fd_to_metric_families(StringIO(response.text)):
            for sample in family.samples:
                labels_dict = {k: v for k, v in sample.labels.items()}
                metrics.append({
                    "Name": sample.name,
                    "Labels": labels_dict,
                    "Value": float(sample.value),
                })
    except Exception as e:
        print(f"[ERROR] Failed to scrape {target}: {e}")
    return metrics

def log_speed():
    while True:
        elapsed = time.time() - start_time
        if elapsed > 0:
            print(f"[INGEST SPEED] Sent {total_sent} samples in {elapsed:.2f}s = {total_sent/elapsed:.2f} samples/sec")
        time.sleep(5)


def estimate_timeseries_from_log(query_type="entropy", expected_targets=None):
    """
    Mendeteksi total_timeseries dari nama file log berdasarkan query_type dan jumlah target.
    Contoh format log yang valid:
        promsketch_ingester_log_10000_ts_entropy_samples.txt
        promsketch_ingester_log_64000_ts_entropy_samples.txt
    """
    try:
        files = os.listdir(".")
        candidates = []

        for fname in files:
            # Cocokkan format nama file
            if fname.startswith("promsketch_ingester_log_") and f"_ts_{query_type}_" in fname:
                # Ekstrak total_timeseries dari nama file
                match = re.search(r"log_(\d+)_ts_" + re.escape(query_type), fname)
                if match:
                    total_ts = int(match.group(1))
                    # Jika expected_targets diketahui, hanya masukkan jika divisible
                    if expected_targets and total_ts % expected_targets != 0:
                        continue
                    candidates.append((total_ts, fname))

        if not candidates:
            print(f"[WARN] Tidak ditemukan log file yang cocok untuk query_type={query_type}")
            return None

        # Urutkan berdasarkan total_ts, ambil terbesar
        candidates.sort()
        best_ts, best_file = candidates[-1]
        print(f"[INFO] Detected log file: {best_file} -> total_timeseries = {best_ts}")
        return best_ts

    except Exception as e:
        print(f"[ERROR] Gagal mencari log file: {e}")
        return None

def ingest_data(config_file):
    global total_sent

    try:
        with open(config_file, "r") as f:
            config_data = yaml.safe_load(f)
    except Exception as e:
        print(f"Error loading config file: {e}")
        sys.exit(1)

    targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
    num_targets = len(targets)

    # Get query type from rule_files for log filename matching
    try:
        rule_file = config_data.get("rule_files", [])[0]
        match = re.search(r'samples_(\w+)_concurrent\.yml', rule_file)
        query_type = match.group(1) if match else "entropy"
    except Exception:
        query_type = "entropy"

    total_timeseries = estimate_timeseries_from_log(query_type)
    if total_timeseries is None:
        total_timeseries = num_targets * 1250  # fallback default
        print(f"[WARN] Using fallback total_timeseries = {total_timeseries}")
    else:
        print(f"[INFO] Detected total_timeseries = {total_timeseries}")

    metrics_per_target = total_timeseries // num_targets
    total_expected_metrics = metrics_per_target * num_targets

    RATE_FACTOR = 40000
    MAX_BATCH_SIZE = total_expected_metrics
    BATCH_SEND_INTERVAL_SECONDS = round(total_expected_metrics / RATE_FACTOR, 2)
    if BATCH_SEND_INTERVAL_SECONDS < 0.2:
        BATCH_SEND_INTERVAL_SECONDS = 0.2

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

    threading.Thread(target=log_speed, daemon=True).start()

    while True:
        current_scrape_time = int(time.time() * 1000)
        with ThreadPoolExecutor(max_workers=num_targets) as executor:
            results = executor.map(scrape_target, targets)

        for metric_list in results:
            metrics_buffer.extend(metric_list)

        if (time.time() - last_send_time >= BATCH_SEND_INTERVAL_SECONDS) or (len(metrics_buffer) >= MAX_BATCH_SIZE):
            if metrics_buffer:
                payload = {
                    "Timestamp": current_scrape_time,
                    "Metrics": metrics_buffer,
                }
                try:
                    resp = requests.post(PROMSKETCH_INGEST_ENDPOINT, json=payload, timeout=5)
                    resp.raise_for_status()
                    print(f"✅ Sent {len(metrics_buffer)} metrics to server. Status: {resp.status_code}")
                    total_sent += len(metrics_buffer)
                except Exception as e:
                    print(f"[ERROR] Sending to Go server: {e}")
                metrics_buffer = []
                last_send_time = time.time()

        time.sleep(interval_seconds)

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=str, required=True, help="Path to Prometheus config file")
    args = parser.parse_args()
    ingest_data(args.config)
