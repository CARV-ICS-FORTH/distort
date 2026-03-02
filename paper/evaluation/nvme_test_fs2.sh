#!/usr/bin/env bash

FILE="$1"

if [ -z "$FILE" ]; then
    echo "Usage: $0 /mnt/nvme/fiofile"
    exit 1
fi

RUNTIME=30
IODEPTH=64
JOBS=8

run_fio() {
    fio \
        --name=test \
        --filename="$FILE" \
        --direct=1 \
        --ioengine=libaio \
        --time_based \
        --runtime=$RUNTIME \
        --group_reporting \
        --lat_percentiles=1 \
        --percentile_list=99 \
        --output-format=json \
        "$@"
}

echo "Dropping caches..."
echo 3 | tee /proc/sys/vm/drop_caches > /dev/null

echo "Running 4K Random Read..."
RR=$(run_fio --rw=randread --bs=4k --iodepth=$IODEPTH --numjobs=$JOBS)

echo "Running 4K Random Write..."
RW=$(run_fio --rw=randwrite --bs=4k --iodepth=$IODEPTH --numjobs=$JOBS)

echo "Running 1M Sequential Read..."
SR=$(run_fio --rw=read --bs=1M --iodepth=32 --numjobs=1)

echo "Running 1M Sequential Write..."
SW=$(run_fio --rw=write --bs=1M --iodepth=32 --numjobs=1)

# -------------------------
# Extraction Helpers
# -------------------------

lat_avg_us() {
    echo "$1" | jq '.jobs[0].'"$2"'.clat_ns.mean' | awk '{ printf "%.2f", $1/1000 }'
}

lat_p99_us() {
    echo "$1" | jq '.jobs[0].'"$2"'.clat_ns.percentile["99.000000"]' | awk '{ printf "%.2f", $1/1000 }'
}

bw_mib() {
    echo "$1" | jq '.jobs[0].'"$2"'.bw_bytes/1024/1024'
}

iops() {
    echo "$1" | jq '.jobs[0].'"$2"'.iops'
}

echo
echo "====== FILESYSTEM RESULTS ======"

echo
echo "4K Random Read:"
printf "  IOPS        : %.0f\n" "$(iops "$RR" read)"
printf "  Avg Lat (us): %s\n" "$(lat_avg_us "$RR" read)"
printf "  P99 Lat (us): %s\n" "$(lat_p99_us "$RR" read)"

echo
echo "4K Random Write:"
printf "  IOPS        : %.0f\n" "$(iops "$RW" write)"
printf "  Avg Lat (us): %s\n" "$(lat_avg_us "$RW" write)"
printf "  P99 Lat (us): %s\n" "$(lat_p99_us "$RW" write)"

echo
echo "1M Sequential Read:"
printf "  MiB/s       : %.2f\n" "$(bw_mib "$SR" read)"
printf "  Avg Lat (us): %s\n" "$(lat_avg_us "$SR" read)"
printf "  P99 Lat (us): %s\n" "$(lat_p99_us "$SR" read)"

echo
echo "1M Sequential Write:"
printf "  MiB/s       : %.2f\n" "$(bw_mib "$SW" write)"
printf "  Avg Lat (us): %s\n" "$(lat_avg_us "$SW" write)"
printf "  P99 Lat (us): %s\n" "$(lat_p99_us "$SW" write)"

echo
echo "================================"
