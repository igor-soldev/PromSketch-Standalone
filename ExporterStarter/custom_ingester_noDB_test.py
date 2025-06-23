import yaml
import requests
import time
import sys
from prometheus_client.parser import text_fd_to_metric_families
from io import StringIO
import json

# Configuration for your PromSketch Go Server
PROMSKETCH_GO_URL = "http://localhost:7000"
PROMSKETCH_INGEST_ENDPOINT = f"{PROMSKETCH_GO_URL}/ingest"

def parse_duration(duration_str):
    if duration_str.endswith('ms'):
        return int(duration_str[:-2]) / 1000  # convert to seconds
    elif duration_str.endswith('s'):
        return int(duration_str[:-1])
    elif duration_str.endswith('m'):
        return int(duration_str[:-1]) * 60
    elif duration_str.endswith('h'):
        return int(duration_str[:-1]) * 3600
    else:
        raise ValueError(f"Unsupported duration format: {duration_str}")

def ingest_data(config_file):
    try:
        with open(config_file, "r") as f:
            config_data = yaml.safe_load(f)
    except FileNotFoundError:
        print(f"Error: Config file '{config_file}' not found.")
        sys.exit(1)
    except yaml.YAMLError as e:
        print(f"Error parsing YAML config file '{config_file}': {e}")
        sys.exit(1)

    targets = config_data["scrape_configs"][0]["static_configs"][0]["targets"]
    
    print(f"Starting data ingestion for targets: {targets}")
    
    scrape_interval_str = config_data["scrape_configs"][0].get("scrape_interval", "10s")
    try:
        interval_seconds = parse_duration(scrape_interval_str)
        if interval_seconds <= 0:
            print(f"Warning: scrape_interval '{scrape_interval_str}' is zero or negative. Setting to 1 second.")
            interval_seconds = 1
    except ValueError as e:
        print(f"Configuration Error: {e}. Defaulting scrape_interval to 10 seconds.")
        interval_seconds = 10

    metrics_buffer = []
    last_send_time = time.time()
    BATCH_SEND_INTERVAL_SECONDS = 0.5
    MAX_BATCH_SIZE = 1000000

    # --- THROUGHPUT MEASUREMENT VARIABLES ---
    total_samples_sent = 0
    ingestion_start_time = time.time()
    last_throughput_report_time = time.time()
    THROUGHPUT_REPORT_INTERVAL_SECONDS = 10 # Report throughput every 10 seconds
    # --- END THROUGHPUT MEASUREMENT VARIABLES ---

    try:
        while True:
            current_scrape_time = int(time.time() * 1000)

            for target in targets:
                try:
                    response = requests.get(f"http://{target}/metrics")
                    response.raise_for_status()
                    
                    for family in text_fd_to_metric_families(StringIO(response.text)):
                        for sample in family.samples:
                            labels_dict = {k: v for k, v in sample.labels.items()}
                            
                            metrics_buffer.append({
                                "Name": sample.name,
                                "Labels": labels_dict,
                                "Value": float(sample.value),
                            })

                except requests.exceptions.ConnectionError as e:
                    print(f"Error connecting to exporter {target}: {e}. Make sure fake exporters are running.")
                except Exception as e:
                    print(f"An unexpected error occurred for target {target}: {e}")
            
            if (time.time() - last_send_time >= BATCH_SEND_INTERVAL_SECONDS) or (len(metrics_buffer) >= MAX_BATCH_SIZE):
                if len(metrics_buffer) > 0:
                    payload = {
                        "Timestamp": current_scrape_time, 
                        "Metrics": metrics_buffer,
                    }
                    try:
                        resp = requests.post(PROMSKETCH_INGEST_ENDPOINT, json=payload, timeout=5)
                        resp.raise_for_status()
                        
                        # --- UPDATE THROUGHPUT COUNTER ---
                        total_samples_sent += len(metrics_buffer) 
                        # --- END UPDATE THROUGHPUT COUNTER ---
                        
                        print(f"Sent {len(metrics_buffer)} metrics to PromSketch Go server. Response: {resp.status_code}")
                    except requests.exceptions.RequestException as e:
                        print(f"Error sending data to PromSketch Go server: {e}")
                    
                    metrics_buffer = []
                    last_send_time = time.time()
            
            # --- PERIODIC THROUGHPUT REPORT ---
            if (time.time() - last_throughput_report_time >= THROUGHPUT_REPORT_INTERVAL_SECONDS):
                elapsed_time_for_report = time.time() - ingestion_start_time
                if elapsed_time_for_report > 0:
                    current_throughput = total_samples_sent / elapsed_time_for_report
                    print(f"Current Ingestion Throughput: {current_throughput:.2f} samples/s (Total: {total_samples_sent} samples over {elapsed_time_for_report:.2f}s)")
                last_throughput_report_time = time.time()
            # --- END PERIODIC THROUGHPUT REPORT ---

            time.sleep(interval_seconds)

    except KeyboardInterrupt:
        # --- FINAL THROUGHPUT REPORT ON EXIT ---
        print("\nIngestion stopped by user (Ctrl+C).")
        final_elapsed_time = time.time() - ingestion_start_time
        if final_elapsed_time > 0:
            final_throughput = total_samples_sent / final_elapsed_time
            print(f"Final Ingestion Throughput: {final_throughput:.2f} samples/s (Total: {total_samples_sent} samples over {final_elapsed_time:.2f}s)")
        else:
            print("No samples sent.")
        # --- END FINAL THROUGHPUT REPORT ---
        sys.exit(0)


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Custom Data Ingester for PromSketch")
    parser.add_argument("--config", type=str, required=True, help="Path to Prometheus config file (e.g., num_samples_config.yml)")
    args = parser.parse_args()

    ingest_data(args.config)