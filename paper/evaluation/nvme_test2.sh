#!/usr/bin/env bash

# Usage:
#   ./nvme_test.sh /dev/nvme1n1
#
# WARNING:
#   This WILL destroy data if you use a block device.

TARGET="$1"

if [ -z "$TARGET" ]; then
    echo "Usage: $0 <device-or-file>"
    exit 1
fi

RUNTIME=30
IODEPTH=64
JOBS=8

run_fio() {
    fio \
        --name=test \
        --filename="$TARGET" \
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

echo "Testing target: $TARGET"
echo

echo "Running 4K Random Read IOPS test..."
RR_JSON=$(run_fio --rw=randread --bs=4k --iodepth=$IODEPTH --numjobs=$JOBS)

echo "Running 4K Random Write IOPS test..."
RW_JSON=$(run_fio --rw=randwrite --bs=4k --iodepth=$IODEPTH --numjobs=$JOBS)

echo "Running 1M Sequential Read Bandwidth test..."
SR_JSON=$(run_fio --rw=read --bs=1M --iodepth=32 --numjobs=1)

echo "Running 1M Sequential Write Bandwidth test..."
SW_JSON=$(run_fio --rw=write --bs=1M --iodepth=32 --numjobs=1)


# -------------------------
# Extraction Helpers
# -------------------------

extract_read_iops() {
    echo "$1" | jq '.jobs[0].read.iops'
}

extract_write_iops() {
    echo "$1" | jq '.jobs[0].write.iops'
}

extract_read_bw() {
    echo "$1" | jq '.jobs[0].read.bw_bytes' | awk '{ printf "%.2f", $1/1024/1024 }'
}

extract_write_bw() {
    echo "$1" | jq '.jobs[0].write.bw_bytes' | awk '{ printf "%.2f", $1/1024/1024 }'
}

extract_read_avg_lat_us() {
    echo "$1" | jq '.jobs[0].read.clat_ns.mean' | awk '{ printf "%.2f", $1/1000 }'
}

extract_write_avg_lat_us() {
    echo "$1" | jq '.jobs[0].write.clat_ns.mean' | awk '{ printf "%.2f", $1/1000 }'
}

extract_read_p99_lat_us() {
    echo "$1" | jq '.jobs[0].read.clat_ns.percentile["99.000000"]' | awk '{ printf "%.2f", $1/1000 }'
}

extract_write_p99_lat_us() {
    echo "$1" | jq '.jobs[0].write.clat_ns.percentile["99.000000"]' | awk '{ printf "%.2f", $1/1000 }'
}


# -------------------------
# Results Output
# -------------------------

echo
echo "=============================="
echo "           RESULTS"
echo "=============================="

echo
echo "4K Random Read:"
printf "  IOPS        : %.0f\n" "$(extract_read_iops "$RR_JSON")"
printf "  Avg Lat (us): %s\n" "$(extract_read_avg_lat_us "$RR_JSON")"
printf "  P99 Lat (us): %s\n" "$(extract_read_p99_lat_us "$RR_JSON")"

echo
echo "4K Random Write:"
printf "  IOPS        : %.0f\n" "$(extract_write_iops "$RW_JSON")"
printf "  Avg Lat (us): %s\n" "$(extract_write_avg_lat_us "$RW_JSON")"
printf "  P99 Lat (us): %s\n" "$(extract_write_p99_lat_us "$RW_JSON")"

echo
echo "1M Sequential Read:"
printf "  BW (MiB/s)  : %s\n" "$(extract_read_bw "$SR_JSON")"
printf "  Avg Lat (us): %s\n" "$(extract_read_avg_lat_us "$SR_JSON")"
printf "  P99 Lat (us): %s\n" "$(extract_read_p99_lat_us "$SR_JSON")"

echo
echo "1M Sequential Write:"
printf "  BW (MiB/s)  : %s\n" "$(extract_write_bw "$SW_JSON")"
printf "  Avg Lat (us): %s\n" "$(extract_write_avg_lat_us "$SW_JSON")"
printf "  P99 Lat (us): %s\n" "$(extract_write_p99_lat_us "$SW_JSON")"

echo
echo "=============================="
