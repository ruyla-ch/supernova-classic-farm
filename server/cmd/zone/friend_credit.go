package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func (h *runtimeHandler) friendTaskCredit(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "LOOPBACK_ONLY")
		return
	}
	playerID, err := strconv.ParseUint(r.PathValue("player_id"), 10, 64)
	if err != nil || playerID == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PLAYER_ID")
		return
	}
	shardValue, err := parseRequiredUintHeader(r, "X-Shard-ID")
	if err != nil || shardValue >= uint64(routing.ShardCount) {
		writeError(w, http.StatusBadRequest, "INVALID_SHARD_ID")
		return
	}
	shardID := uint32(shardValue)
	unlockShard := h.gates.readLock(shardID)
	defer unlockShard()
	ownerZoneID := r.Header.Get("X-Owner-Zone-ID")
	ownerEpoch, err := parseRequiredUintHeader(r, "X-Owner-Epoch")
	if err != nil || ownerZoneID == "" || ownerEpoch == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_OWNERSHIP")
		return
	}
	if h.authorization == nil {
		writeError(w, http.StatusServiceUnavailable, "OWNERSHIP_UNAVAILABLE")
		return
	}
	if err := h.authorization.Validate(playerID, shardID, ownerZoneID, ownerEpoch, h.now()); err != nil {
		writeNotOwner(w, h.authorization, shardID)
		return
	}
	if err := h.runtime.ApplyFriendTaskCredit(r.Context(), playerID, ownerEpoch); err != nil {
		if err == player.ErrNotOwner {
			writeNotOwner(w, h.authorization, shardID)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "CREDIT_FAILED")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}
