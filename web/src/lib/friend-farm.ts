export type StateVersionLike = {
  ownerEpoch: bigint
  playerSeq: bigint
}

export function isNewerStateVersion(
  candidate: StateVersionLike,
  current: StateVersionLike,
): boolean {
  return (
    candidate.ownerEpoch > current.ownerEpoch ||
    (candidate.ownerEpoch === current.ownerEpoch && candidate.playerSeq > current.playerSeq)
  )
}

export function mergePublicPlotUpserts<T extends { plotId: number }>(
  current: readonly T[],
  upserts: readonly T[],
): T[] {
  const plots = new Map(current.map((plot) => [plot.plotId, plot]))
  for (const plot of upserts) {
    plots.set(plot.plotId, plot)
  }
  return [...plots.values()].sort((left, right) => left.plotId - right.plotId)
}
