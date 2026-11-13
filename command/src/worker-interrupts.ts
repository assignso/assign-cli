let scopes = 0;

export function hasWorkerInterruptScope(): boolean {
  return scopes > 0;
}

export function scopeWorkerInterrupts(controller: AbortController): () => void {
  const stopInt = () => controller.abort(new Error("Worker interrupted by SIGINT"));
  const stopTerm = () => controller.abort(new Error("Worker interrupted by SIGTERM"));
  scopes++;
  process.on("SIGINT", stopInt);
  process.on("SIGTERM", stopTerm);
  return () => {
    process.off("SIGINT", stopInt);
    process.off("SIGTERM", stopTerm);
    scopes--;
  };
}
