# Machine cache retention

Jython retains the two newest timestamped corrupt samples per version. Cleanup
runs at the end of each cache access, under the existing per-version lock,
including accesses that find a verified jar. The verified jar, lock file,
unknown files, directories and symlinks are excluded.

Private-module lookup indexes expire 90 days after their last atomic save,
based on file modification time. A successful index save performs best-effort
cleanup of recognized index filenames in that Ignition version's modules
directory. Cache hits do not update modification time. An old index still in
use may expire and will be rebuilt from the checkout's staged archive on its
next lookup. Recent indexes from all checkouts remain eligible for reuse,
regardless of what the checkout performing cleanup stages.

The age threshold limits ordinary build churn without tracking access times or
imposing a count cap on concurrently active checkouts. Cleanup is opportunistic;
an idle cache does not shrink until a subsequent save. It never scans other
versions, image or REST indexes, Gateway data, or the whole machine cache.

Read, stat and deletion failures during pruning are ignored. No reclaimed counts
are reported. Lookup readers treat a missing index as a cache miss, and saves
continue to use atomic replacement. Retention adds no CLI options, envelope
fields, error codes or exit levels.
