# General Information

- Run the files in the same location of where the prometheus binary is stored at
- Scrape intveral is currently 20 seconds

### ExportManager.py

- `python ExportManager.py --targets=numtargets --config="samplewindowconfig.yml"`
- Starts `numtargets` fake exporters and also starts prometheus binary
- Edits `samplewindowconfig` or the prometheus config to handle `numtargets` as scrape targets
- Automatically quits after starting instances
  - To kill the experiment run `ps aux | grep python`, `ps aux | grep prometheus`
  - Get the pids of the fake_exporters and prometheus and run `kill pid`

### fake_norm_exporter.py

- `python fake_norm_exporter.py --port=port --instancestart=machineidstart --valuescale=valuescale`
- `instancestart` represents the machineid the fake exporter will start from all the way to instancestart+500
- `valuescale` the max value the exporter will report from 0 - valuescale in a normal distribution format

### samplewindowconfig

- The prometheus config
