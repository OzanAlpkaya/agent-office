#!/bin/sh
# Turns the recorded team run into the README's GIF (docs/demo.md): from the "lead started"
# marker to the end, sped up to about TARGET seconds, 800 px wide, 6 fps, 64 colours (about 6 MB
# for a one-minute run; GitHub shows GIFs up to 10 MB inline).
#   web/demo/to-gif.sh <out dir of record-team-run.ts> <gif> [target seconds, default 35]
set -eu
dir=$1
gif=$2
target=${3:-35}
webm="$dir/team-run.webm"
# Start a second before the Tech Lead's button is clicked, so the goal on the board shows.
start=$(python3 -I -c 'import json,sys; m=json.load(open(sys.argv[1])); print(max(0, m["lead started"] - 6))' "$dir/markers.json")
duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$webm")
speed=$(python3 -I -c 'import sys; d=float(sys.argv[1])-float(sys.argv[2]); print(max(1.0, d/float(sys.argv[3])))' "$duration" "$start" "$target")
echo "start ${start}s, length ${duration}s, speed ${speed}x"
ffmpeg -v error -y -ss "$start" -i "$webm" -an -vf \
  "setpts=PTS/${speed},fps=6,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
  -loop 0 "$gif"
ls -l "$gif"
