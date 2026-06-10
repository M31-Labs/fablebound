package pool

// worker.go holds the types and helpers for pool worker execution.
// The main execute logic lives in pool.go (Pool.executeDispatch).
// This file is intentionally minimal: the pool uses the workflow.WorkerHandler
// interface from arbiter directly rather than introducing a parallel type hierarchy.
