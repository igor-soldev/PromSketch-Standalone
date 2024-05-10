from prometheus_client.core import GaugeMetricFamily, CounterMetricFamily, REGISTRY
from prometheus_client import start_http_server
from prometheus_client.registry import Collector
from prometheus_client import PROCESS_COLLECTOR, PLATFORM_COLLECTOR, GC_COLLECTOR
import argparse
import random
import sys


class CustomCollector(Collector):

    def __init__(self, num_machines, scale, start):
        self.num_machines = num_machines
        self.scale = scale
        self.start = start

    def collect(self):

        fake_metric = GaugeMetricFamily(
            "fake_machine_metric",
            "fake_machine_normal_distibution",
            labels=["machineid"],
        )
        for i in range(self.start, self.start + self.num_machines):
            value = -1
            while value < 0:
                value = random.gauss(0.5, 0.2) * self.scale

            fake_metric.add_metric([f"machine_{i}"], value=value)

        yield fake_metric


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Process metric data")
    parser.add_argument("--port", type=int, help="port to start on")
    parser.add_argument("--instancestart", type=int, help="instance_id to start on ")
    parser.add_argument(
        "--valuescale", type=int, help="range of report metric 0-valuescale"
    )
    args = parser.parse_args()
    if args.port is None or args.valuescale is None or args.instancestart is None:
        print("Missing argument --port, or --valuescale or --instancestart")
        sys.exit(0)
    print("Starting Server ...")
    NUMBER_TIMESERIES = 500
    metric_collector = CustomCollector(
        NUMBER_TIMESERIES, args.valuescale, args.instancestart
    )
    REGISTRY.unregister(PROCESS_COLLECTOR)
    REGISTRY.unregister(PLATFORM_COLLECTOR)
    REGISTRY.unregister(GC_COLLECTOR)
    REGISTRY.register(metric_collector)
    start_http_server(port=args.port)
    print("Server Started")
    while True:
        a = 5
