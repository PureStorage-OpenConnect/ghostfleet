import type { RunSample } from './api'

const MiB = 1024 * 1024

// Minimum span a rate is averaged over. Agents report every 5s and the
// controller samples every 5s, so a single interval aliases (0, 2x, 0, ...);
// averaging over two intervals smooths that out.
const WINDOW_SEC = 10

// throughputSeries turns a run's samples into a MB/s series (MiB-based, like
// the run summary's avgMBps) for the sparkline.
//
// The stored mbps is a snapshot of the agents' instantaneous rate, summed over
// VMs still working. On short runs it is mostly 0: a VM writes its few GiB
// between two snapshots and reports done before the next one. The cumulative
// bytesWritten in every sample doesn't miss anything, so the rate is derived
// from it and the snapshot is used only when it is higher (a real
// instantaneous burst).
export function throughputSeries(samples: RunSample[]): number[] {
  const t = samples.map((s) => Date.parse(s.ts) / 1000)
  let lo = 0
  return samples.map((s, i) => {
    while (lo < i - 1 && t[i] - t[lo + 1] >= WINDOW_SEC) lo++
    let derived = 0
    const dt = t[i] - t[lo]
    if (i > 0 && dt > 0) {
      derived = Math.max(0, s.bytesWritten - samples[lo].bytesWritten) / MiB / dt
    }
    return Math.max(derived, s.mbps)
  })
}
