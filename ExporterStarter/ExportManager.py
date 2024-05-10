import yaml
import argparse
import sys
import subprocess

ports = []
processes = []
START_PORT = 8000


def define_targets(file):
    with open(file, "r") as f:
        config_data = yaml.safe_load(f)
    prefix = "localhost:"
    res = []
    for port in ports:
        res.append(prefix + str(port))
    config_data["scrape_configs"][0]["static_configs"][0]["targets"] = res

    with open(file, "w") as f:
        yaml.dump(config_data, f, default_flow_style=True)


def create_ports(targets):
    for i in range(targets):
        port = START_PORT + i
        ports.append(port)


def start_subprocesses(config):
    for port in ports:
        starting_val = (port - START_PORT) * 500
        process = subprocess.Popen(
            [
                sys.executable,
                "fake_norm_exporter.py",
                f"--port={str(port)}",
                f"--valuescale=10",
                f"--instancestart={str(starting_val)}",
            ]
        )
        processes.append(process)
    process = subprocess.Popen(
        [
            "./prometheus",
            f"--config.file={config}",
        ]
    )
    processes.append(process)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="process config file")
    parser.add_argument("--config", type=str, help="config")
    parser.add_argument("--targets", type=int, help="number of targets")
    args = parser.parse_args()
    if args.config is None or args.targets is None:
        print("Missing Config or number of targets, --targets=int --config=str ")
        sys.exit(0)

    config_file = args.config
    targets = args.targets
    create_ports(targets)
    define_targets(config_file)
    start_subprocesses(config_file)
