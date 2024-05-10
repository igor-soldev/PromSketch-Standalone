# General Infomration

- Contains both the evaluation script and drawing notebook

### EvalData.py

- `python EvalData.py --targets=number_targets_evaluating --waiteval=waittime`
- Requests `http://localhost:9090/api/v1/rules` 10 times waiting `waiteval` before requests and recording the evaluation time again
- Utilizes pandas to write csv for called `targets_{targets}_data.csv`
  - Contains cols `Sample_Size,Quantile,Sum,Average`
- Uses `targets_{targets}_data.csv` and aggregates and averages to add new row to existing `timeseries.csv`
  - `timeseries.csv` contains cols `Monitoring_Targets,Quantile,Sum,Average`
  - `timeseries` utlizes the max sample size of 10000

### Example Response From `http://localhost:9090/api/v1/rules`

````{
  "status": "success",
  "data": {
    "groups": [
      {
        "name": "fake_metric_100000_samples",
        "file": "100000samples.yml",
        "rules": [
          {
            "name": "fake_metric_avg",
            "query": "avg_over_time(fake_machine_metric[1d3h46m40s])",
            "health": "ok",
            "evaluationTime": 0.129411947,
            "lastEvaluation": "2024-04-29T11:58:42.942403492-05:00",
            "type": "recording"
          },
          {
            "name": "fake_metric_sum",
            "query": "sum_over_time(fake_machine_metric[1d3h46m40s])",
            "health": "ok",
            "evaluationTime": 0.124343052,
            "lastEvaluation": "2024-04-29T11:58:43.071825888-05:00",
            "type": "recording"
          },
          {
            "name": "fake_metric_quantile",
            "query": "quantile_over_time(0.5, fake_machine_metric[1d3h46m40s])",
            "health": "ok",
            "evaluationTime": 0.285156309,
            "lastEvaluation": "2024-04-29T11:58:43.196181769-05:00",
            "type": "recording"
          }
        ],
        "interval": 20,
        "limit": 0,
        "evaluationTime": 0.538978733,
        "lastEvaluation": "2024-04-29T11:58:42.942365602-05:00"
      },
}```
````
