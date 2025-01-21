echo "--windowsize=10 --querytype=distinct"
python ExportManager.py --config=num_samples_config.yml --targets=200 --timeseries=10000 --max_windowsize=1000000 --querytype=distinct --waiteval=300

./kill.sh
