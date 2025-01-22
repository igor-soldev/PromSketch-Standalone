from prometheus_client.core import GaugeMetricFamily, CounterMetricFamily, REGISTRY
from prometheus_client import start_http_server
from prometheus_client.registry import Collector
from prometheus_client import PROCESS_COLLECTOR, PLATFORM_COLLECTOR, GC_COLLECTOR
import argparse
import random
import sys
import time
import numpy

batch_size = 1
caida_length = 20000000

class CustomCollector(Collector):

    def __init__(self, num_machines, scale, machine_id_start):
        self.num_machines = num_machines
        self.scale = scale
        self.machine_id_start = machine_id_start
        self.rng = numpy.random.default_rng()
        self.total_samples = 0
        self.caida_dataset = []
        filename = "datasets/caida2019_sourceip.txt"
        with open(filename, "r") as f:
            lines = f.readlines()
            for line in lines:
                self.caida_dataset.append(int(line))

    def collect(self):

        fake_metric = GaugeMetricFamily(
            "fake_machine_metric",
            "Generating fake machine time series data with normal distibution",
            labels=["machineid"],
        )
        for i in range(
            self.machine_id_start, self.machine_id_start + self.num_machines
        ):
            value = self.caida_dataset[self.total_samples % caida_length]
            self.total_samples += 1
            self.total_samples = self.total_samples % const_3M
            fake_metric.add_metric([f"machine_{i}"], value=value)

        yield fake_metric


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Process metric data")
    parser.add_argument("--port", type=int, help="port to start on")
    parser.add_argument("--instancestart", type=int, help="machine_id to start on ")
    parser.add_argument(
        "--valuescale", type=int, help="range of report metric 0-valuescale"
    )
    parser.add_argument(
        "--batchsize",
        type=int,
        help="machine number (timeseries number) for each target to generate",
    )
    args = parser.parse_args()
    if (
        args.port is None
        or args.valuescale is None
        or args.instancestart is None
        or args.batchsize is None
    ):
        print("Missing argument --port, or --valuescale or --instancestart")
        sys.exit(0)
    # print("Starting Server ...")
    metric_collector = CustomCollector(
        args.batchsize, args.valuescale, args.instancestart
    )
    REGISTRY.unregister(PROCESS_COLLECTOR)
    REGISTRY.unregister(PLATFORM_COLLECTOR)
    REGISTRY.unregister(GC_COLLECTOR)
    REGISTRY.register(metric_collector)
    start_http_server(port=args.port)
    # print("Server Started")
    while True:
        time.sleep(1)