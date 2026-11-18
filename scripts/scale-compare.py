import argparse
import json
import os
from pathlib import Path
import re
import shutil
import statistics
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--baseline', type=Path, required=True)
parser.add_argument('--output', type=Path, required=True)
parser.add_argument('--url', required=True)
args = parser.parse_args()
current = Path(__file__).resolve().parents[1]
args.output.mkdir(parents=True, exist_ok=True)
env = dict(os.environ, ARCHIVE_SCALE_URL=args.url)
sides = [('old', args.baseline), ('new', current)]


def disk_check():
    if shutil.disk_usage('/System/Volumes/Data').free < 3 * 1024**3:
        raise RuntimeError('stopping below 3 GiB free')


for name, cwd in sides:
    disk_check()
    subprocess.run(['go', 'test', '-c', './internal/archive', '-o',
                    str(args.output / f'{name}.test')], cwd=cwd, env=env, check=True)

rows = []
pattern = r'SCALE name=(\w+) rows=(\d+) bytes=(\d+) seconds=([\d.]+) result_bytes=(\d+)'
for run in range(5):
    for name, cwd in sides:
        disk_check()
        result = subprocess.run([str(args.output / f'{name}.test'),
                                 '-test.run=^TestClickHouseScale$', '-test.v'],
                                cwd=cwd, env=env, capture_output=True, text=True)
        (args.output / f'{name}-{run}.txt').write_text(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError(result.stdout + result.stderr)
        matches = re.findall(pattern, result.stdout)
        if len(matches) != 8:
            raise RuntimeError(f'{name} run {run} did not measure every endpoint')
        for endpoint, count, size, seconds, response in matches:
            rows.append(dict(side=name, run=run, endpoint=endpoint, rows=int(count),
                             bytes=int(size), seconds=float(seconds), result_bytes=int(response)))

(args.output / 'results.json').write_text(json.dumps(rows, indent=2))
for endpoint in dict.fromkeys(row['endpoint'] for row in rows):
    for side, _ in sides:
        values = [row for row in rows if row['endpoint'] == endpoint and row['side'] == side]
        medians = [statistics.median(row[key] for row in values) for key in ('rows', 'bytes', 'seconds')]
        print(endpoint, side, *medians, min(row['seconds'] for row in values),
              max(row['seconds'] for row in values), flush=True)
