package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
	"time"

	httpv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/http"
	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	plotv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/plot"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"github.com/coder/websocket"
	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/proto"
)

type friendE2EPlayer struct {
	accountName string
	playerID    uint64
	conn        *websocket.Conn
}

type friendRecoveryState struct {
	OwnerAccount    string `json:"owner_account"`
	OwnerPlayerID   uint64 `json:"owner_player_id"`
	VisitorAccount  string `json:"visitor_account"`
	VisitorPlayerID uint64 `json:"visitor_player_id"`
	CropItemID      uint32 `json:"crop_item_id"`
	StolenQuantity  uint32 `json:"stolen_quantity"`
	ActiveVisitID   []byte `json:"active_visit_id"`
}

func TestMySQLFriendSlice(t *testing.T) {
	if os.Getenv("E2E_FRIEND_RUN") != "1" {
		t.Skip("use tests/e2e/run-mysql-friend-slice.ps1")
	}
	if os.Getenv("E2E_FRIEND_PHASE") == "recover" {
		verifyFriendRecovery(t)
		return
	}
	loginURL := envOr("E2E_LOGIN_URL", defaultLoginURL)
	first := registerFriendE2EPlayer(t, loginURL)
	defer first.conn.CloseNow()
	firstOwner := routeOwner(t, first.playerID)

	var second friendE2EPlayer
	for attempt := 0; attempt < 16; attempt++ {
		candidate := registerFriendE2EPlayer(t, loginURL)
		if routeOwner(t, candidate.playerID) != firstOwner {
			second = candidate
			break
		}
		candidate.conn.CloseNow()
	}
	if second.playerID == 0 {
		t.Fatal("could not register two players assigned to different Zones")
	}
	defer second.conn.CloseNow()
	secondOwner := routeOwner(t, second.playerID)
	t.Logf("PLAYERS first=%d/%s second=%d/%s", first.playerID, firstOwner, second.playerID, secondOwner)
	// The browser loads its own authoritative snapshot after AUTH. Mirror that
	// barrier so unsolicited visitor-targeted Pushes are no longer buffered.
	_ = playerSnapshot(t, second)

	createResponse := friendRequest(t, first, wsv1.Action_CREATE_FRIEND_CODE,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_CreateFriendCodeRequest{
				CreateFriendCodeRequest: &wsv1.CreateFriendCodeRequest{},
			}
		})
	code := createResponse.GetCreateFriendCodeResponse().GetCode()
	if len(code) != 32 {
		t.Fatalf("friend code length=%d", len(code))
	}

	redeemPayload := func(request *wsv1.WsEnvelope) {
		request.Payload = &wsv1.WsEnvelope_RedeemFriendCodeRequest{
			RedeemFriendCodeRequest: &wsv1.RedeemFriendCodeRequest{Code: code},
		}
	}
	redeemResponse := friendRequest(t, second, wsv1.Action_REDEEM_FRIEND_CODE, redeemPayload)
	redeemed := redeemResponse.GetRedeemFriendCodeResponse()
	if !redeemed.GetNewlyCreated() ||
		redeemed.GetFriend().GetPlayerId() != first.playerID ||
		redeemed.GetFriend().GetAccountName() != first.accountName {
		t.Fatalf("unexpected redeem response: %+v", redeemResponse)
	}

	assertFriendList(t, first, second)
	assertFriendList(t, second, first)

	replayResponse := friendRequest(t, second, wsv1.Action_REDEEM_FRIEND_CODE, redeemPayload)
	if replayResponse.GetRedeemFriendCodeResponse().GetNewlyCreated() {
		t.Fatal("idempotent redeem reported newly_created=true")
	}
	assertPersistedFriendRelation(t, first.playerID, second.playerID)

	third := registerFriendE2EPlayer(t, loginURL)
	defer third.conn.CloseNow()
	_ = playerSnapshot(t, third)
	createThird := friendRequest(t, first, wsv1.Action_CREATE_FRIEND_CODE,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_CreateFriendCodeRequest{
				CreateFriendCodeRequest: &wsv1.CreateFriendCodeRequest{},
			}
		})
	thirdCode := createThird.GetCreateFriendCodeResponse().GetCode()
	friendRequest(t, third, wsv1.Action_REDEEM_FRIEND_CODE,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_RedeemFriendCodeRequest{
				RedeemFriendCodeRequest: &wsv1.RedeemFriendCodeRequest{Code: thirdCode},
			}
		})
	assertPersistedFriendRelation(t, first.playerID, third.playerID)

	prepareGrowingFriendCrop(t, first)
	visitResponse := friendRequest(t, second, wsv1.Action_ENTER_FRIEND_FARM,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_EnterFriendFarmRequest{
				EnterFriendFarmRequest: &wsv1.EnterFriendFarmRequest{
					OwnerPlayerId: first.playerID,
				},
			}
		})
	visit := visitResponse.GetEnterFriendFarmResponse()
	plot := publicPlot(visit.GetSnapshot(), 1)
	if len(visit.GetVisitId()) != 16 || plot == nil ||
		plot.GetPlotState() != plotv1.PlotState_GROWING {
		t.Fatalf("unexpected visit response: %+v", visitResponse)
	}
	thirdVisit := friendRequest(t, third, wsv1.Action_ENTER_FRIEND_FARM,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_EnterFriendFarmRequest{
				EnterFriendFarmRequest: &wsv1.EnterFriendFarmRequest{
					OwnerPlayerId: first.playerID,
				},
			}
		}).GetEnterFriendFarmResponse()
	thirdPlot := publicPlot(thirdVisit.GetSnapshot(), 1)
	if len(thirdVisit.GetVisitId()) != 16 ||
		thirdPlot == nil || thirdPlot.GetPlotState() != plotv1.PlotState_GROWING {
		t.Fatalf("unexpected third visit response: %+v", thirdVisit)
	}
	friendRequest(t, second, wsv1.Action_FARM_HEARTBEAT,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_FarmHeartbeatRequest{
				FarmHeartbeatRequest: &wsv1.FarmHeartbeatRequest{
					OwnerPlayerId: first.playerID, VisitId: visit.GetVisitId(),
				},
			}
		})
	friendRequest(t, third, wsv1.Action_FARM_HEARTBEAT,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_FarmHeartbeatRequest{
				FarmHeartbeatRequest: &wsv1.FarmHeartbeatRequest{
					OwnerPlayerId: first.playerID, VisitId: thirdVisit.GetVisitId(),
				},
			}
		})
	assertLivePestGameplay(t, first, second, third, visit, thirdVisit)
	friendRequest(t, third, wsv1.Action_EXIT_FRIEND_FARM,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_ExitFriendFarmRequest{
				ExitFriendFarmRequest: &wsv1.ExitFriendFarmRequest{
					OwnerPlayerId: first.playerID, VisitId: thirdVisit.GetVisitId(),
				},
			}
		})
	plot = finishMatureFriendCrop(t, first, second, visit.GetVisitId())
	if plot == nil || !plot.GetCanSteal() || plot.GetStealQuantity() != 1 {
		t.Fatalf("unexpected mature public plot: %+v", plot)
	}
	stealRequestID := newUUID(t)
	stealPayload := func(request *wsv1.WsEnvelope) {
		request.Payload = &wsv1.WsEnvelope_StealFriendCropRequest{
			StealFriendCropRequest: &wsv1.StealFriendCropRequest{
				OwnerPlayerId: first.playerID, VisitId: visit.GetVisitId(),
				PlotId: plot.GetPlotId(), ExpectedCropItemId: plot.GetCropItemId(),
				ExpectedPlantedAtMs:   plot.GetPlantedAtMs(),
				ExpectedStealQuantity: plot.GetStealQuantity(),
			},
		}
	}
	steal, observedPushes := friendRequestWithIDObserving(
		t, second, stealRequestID, wsv1.Action_STEAL_FRIEND_CROP, stealPayload,
	)
	ownerStealPush := matchingPush(t, first, nil, func(push *wsv1.WsEnvelope) bool {
		change := push.GetPlayerStateChangedPush()
		return push.GetMessageKind() == wsv1.MessageKind_PUSH &&
			push.GetAction() == wsv1.Action_PLAYER_STATE_CHANGED &&
			push.GetTargetPlayerId() == first.playerID &&
			change.GetReason() == reasonv1.StateChangeReason_FRIEND_STEAL &&
			change.GetCausedByRequestId() == stealRequestID
	}, "owner steal push")
	friendFarmPush := matchingPush(t, second, observedPushes,
		func(push *wsv1.WsEnvelope) bool {
			change := push.GetFriendFarmChangedPush()
			return push.GetMessageKind() == wsv1.MessageKind_PUSH &&
				push.GetAction() == wsv1.Action_FRIEND_FARM_CHANGED &&
				push.GetTargetPlayerId() == second.playerID &&
				change.GetOwnerPlayerId() == first.playerID &&
				bytes.Equal(change.GetVisitId(), visit.GetVisitId()) &&
				sameVersion(change.GetOwnerStateVersion(), ownerStealPush.GetStateVersion()) &&
				len(change.GetPlotUpserts()) == 1 &&
				change.GetPlotUpserts()[0].GetStealCount() == 1
		}, "visitor steal push")
	pushedChange := friendFarmPush.GetFriendFarmChangedPush()
	ownerChange := ownerStealPush.GetPlayerStateChangedPush()
	if !sameVersion(ownerStealPush.GetStateVersion(), pushedChange.GetOwnerStateVersion()) ||
		len(ownerChange.GetPatch().GetPlotUpserts()) != 1 ||
		ownerChange.GetPatch().GetPlotUpserts()[0].GetHarvestableQuantity() != 2 {
		t.Fatalf("unexpected owner steal push: %+v", ownerStealPush)
	}
	replaySteal := friendRequestWithID(
		t, second, stealRequestID, wsv1.Action_STEAL_FRIEND_CROP, stealPayload,
	)
	if !replaySteal.GetReplayed() ||
		!proto.Equal(steal.GetStealFriendCropResponse(), replaySteal.GetStealFriendCropResponse()) {
		t.Fatalf("steal replay mismatch first=%+v replay=%+v", steal, replaySteal)
	}
	assertPlayerHasItem(t, second, plot.GetCropItemId(), plot.GetStealQuantity())
	friendRequest(t, second, wsv1.Action_EXIT_FRIEND_FARM,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_ExitFriendFarmRequest{
				ExitFriendFarmRequest: &wsv1.ExitFriendFarmRequest{
					OwnerPlayerId: first.playerID, VisitId: visit.GetVisitId(),
				},
			}
		})
	restartVisit := friendRequest(t, second, wsv1.Action_ENTER_FRIEND_FARM,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_EnterFriendFarmRequest{
				EnterFriendFarmRequest: &wsv1.EnterFriendFarmRequest{
					OwnerPlayerId: first.playerID,
				},
			}
		}).GetEnterFriendFarmResponse()
	if len(restartVisit.GetVisitId()) != 16 {
		t.Fatalf("restart-boundary visit=%+v", restartVisit)
	}
	time.Sleep(2 * time.Second)
	writeFriendRecoveryState(t, friendRecoveryState{
		OwnerAccount: first.accountName, OwnerPlayerID: first.playerID,
		VisitorAccount: second.accountName, VisitorPlayerID: second.playerID,
		CropItemID: plot.GetCropItemId(), StolenQuantity: plot.GetStealQuantity(),
		ActiveVisitID: restartVisit.GetVisitId(),
	})
	t.Log("FRIEND create_redeem_list=true visit_heartbeat_exit=true pest_apply=true pest_replay=true pest_source_forbidden=true owner_catch_pest=true friend_catch_pest=true pest_owner_push=true pest_all_visitors_push=true direct_steal=true visitor_inventory=true steal_replay=true owner_maturity_push=true owner_steal_push=true visitor_farm_push=true mysql_relation=true")
}

func writeFriendRecoveryState(t *testing.T, state friendRecoveryState) {
	t.Helper()
	path := os.Getenv("E2E_FRIEND_STATE_PATH")
	if path == "" {
		t.Fatal("E2E_FRIEND_STATE_PATH is required")
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifyFriendRecovery(t *testing.T) {
	t.Helper()
	path := os.Getenv("E2E_FRIEND_STATE_PATH")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state friendRecoveryState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	loginURL := envOr("E2E_LOGIN_URL", defaultLoginURL)
	owner := loginFriendE2EPlayer(t, loginURL, state.OwnerAccount)
	defer owner.conn.CloseNow()
	visitor := loginFriendE2EPlayer(t, loginURL, state.VisitorAccount)
	defer visitor.conn.CloseNow()
	if owner.playerID != state.OwnerPlayerID || visitor.playerID != state.VisitorPlayerID {
		t.Fatalf("recovered players owner=%d visitor=%d", owner.playerID, visitor.playerID)
	}
	assertFriendList(t, owner, visitor)
	assertFriendList(t, visitor, owner)
	ownerSnapshot := playerSnapshot(t, owner)
	var ownerPlot *wsv1.PlotView
	for _, plot := range ownerSnapshot.GetPlots() {
		if plot.GetPlotId() == 1 {
			ownerPlot = plot
			break
		}
	}
	if ownerPlot == nil || ownerPlot.GetHarvestableQuantity() != 2 {
		t.Fatalf("recovered owner plot=%+v", ownerPlot)
	}
	assertSnapshotHasItem(
		t, visitor.playerID, playerSnapshot(t, visitor),
		state.CropItemID, state.StolenQuantity,
	)
	requestID := newUUID(t)
	writeEnvelope(t, visitor.conn, &wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_FARM_HEARTBEAT, RequestId: requestID,
		TargetPlayerId: visitor.playerID,
		Payload: &wsv1.WsEnvelope_FarmHeartbeatRequest{
			FarmHeartbeatRequest: &wsv1.FarmHeartbeatRequest{
				OwnerPlayerId: owner.playerID, VisitId: state.ActiveVisitID,
			},
		},
	})
	heartbeat := readEnvelope(t, visitor.conn)
	if heartbeat.GetRequestId() != requestID || heartbeat.GetError().GetCode() !=
		wsv1.ErrorCode_VISIT_NOT_FOUND {
		t.Fatalf("old visit survived restart: %+v", heartbeat)
	}
	assertPersistedFriendRelation(t, owner.playerID, visitor.playerID)
	t.Log("FRIEND_RECOVERY owner_plot=true visitor_inventory=true relation=true old_visit_invalid=true")
}

func prepareGrowingFriendCrop(t *testing.T, owner friendE2EPlayer) {
	t.Helper()
	// Complete Gate's snapshot barrier before mutating so the later unsolicited
	// maturity Push is delivered instead of remaining in the pre-snapshot buffer.
	_ = playerSnapshot(t, owner)
	shop := friendRequest(t, owner, wsv1.Action_GET_SHOP,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_GetShopRequest{
				GetShopRequest: &wsv1.GetShopRequest{},
			}
		}).GetGetShopResponse()
	var seedQuote *wsv1.ShopEntryView
	for _, entry := range shop.GetEntries() {
		if entry.GetItemId() == 1001 {
			seedQuote = entry
			break
		}
	}
	if seedQuote == nil {
		t.Fatal("seed quote not found")
	}
	friendRequest(t, owner, wsv1.Action_BUY_SEEDS,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_BuySeedsRequest{
				BuySeedsRequest: &wsv1.BuySeedsRequest{
					ShopEntryId: seedQuote.GetShopEntryId(), Quantity: 1,
					ExpectedPriceVersion: seedQuote.GetPriceVersion(),
				},
			}
		})
	friendRequest(t, owner, wsv1.Action_PLANT,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_PlantRequest{
				PlantRequest: &wsv1.PlantRequest{PlotId: 1, SeedItemId: 1001},
			}
		})
	snapshot := playerSnapshot(t, owner)
	if plot := snapshotPlot(snapshot, 1); plot == nil ||
		plot.GetPlotState() != plotv1.PlotState_GROWING || plot.GetPestEffect() != nil {
		t.Fatalf("prepared owner plot is not clean GROWING: %+v", plot)
	}
}

func finishMatureFriendCrop(
	t *testing.T,
	owner, visitor friendE2EPlayer,
	visitID []byte,
) *wsv1.PublicPlotView {
	t.Helper()
	friendRequest(t, visitor, wsv1.Action_FARM_HEARTBEAT,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_FarmHeartbeatRequest{
				FarmHeartbeatRequest: &wsv1.FarmHeartbeatRequest{
					OwnerPlayerId: owner.playerID, VisitId: visitID,
				},
			}
		})
	requestID := newUUID(t)
	response, observed := friendRequestWithIDObserving(
		t, owner, requestID, wsv1.Action_APPLY_FERTILIZER,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_ApplyFertilizerRequest{
				ApplyFertilizerRequest: &wsv1.ApplyFertilizerRequest{
					PlotId: 1, FertilizerItemId: 1,
				},
			}
		},
	)
	ownerPush := matchingPush(t, owner, observed, func(push *wsv1.WsEnvelope) bool {
		change := push.GetPlayerStateChangedPush()
		return push.GetAction() == wsv1.Action_PLAYER_STATE_CHANGED &&
			change.GetReason() == reasonv1.StateChangeReason_APPLY_FERTILIZER &&
			change.GetCausedByRequestId() == requestID
	}, "owner fertilizer push")
	if !sameVersion(response.GetStateVersion(), ownerPush.GetStateVersion()) {
		t.Fatalf("fertilizer response/push versions differ response=%+v push=%+v",
			response, ownerPush)
	}
	visitorPush := matchingPush(t, visitor, nil, func(push *wsv1.WsEnvelope) bool {
		change := push.GetFriendFarmChangedPush()
		return push.GetAction() == wsv1.Action_FRIEND_FARM_CHANGED &&
			change.GetOwnerPlayerId() == owner.playerID &&
			bytes.Equal(change.GetVisitId(), visitID) &&
			sameVersion(change.GetOwnerStateVersion(), ownerPush.GetStateVersion())
	}, "visitor fertilizer push")
	if len(visitorPush.GetFriendFarmChangedPush().GetPlotUpserts()) != 1 {
		t.Fatalf("unexpected visitor fertilizer push: %+v", visitorPush)
	}
	// The fertilizer makes the development crop mature in about 70 seconds.
	time.Sleep(75 * time.Second)
	matured := matchingPush(t, owner, nil, func(push *wsv1.WsEnvelope) bool {
		return push.GetMessageKind() == wsv1.MessageKind_PUSH &&
			push.GetAction() == wsv1.Action_PLAYER_STATE_CHANGED &&
			push.GetTargetPlayerId() == owner.playerID &&
			push.GetStateVersion() != nil &&
			push.GetPlayerStateChangedPush().GetReason() ==
				reasonv1.StateChangeReason_MATURED &&
			len(push.GetPlayerStateChangedPush().GetPatch().GetPlotUpserts()) == 1 &&
			push.GetPlayerStateChangedPush().GetPatch().GetPlotUpserts()[0].GetPlotState() ==
				plotv1.PlotState_MATURE
	}, "owner maturity push")
	maturedVisitor := matchingPush(t, visitor, nil, func(push *wsv1.WsEnvelope) bool {
		change := push.GetFriendFarmChangedPush()
		return push.GetAction() == wsv1.Action_FRIEND_FARM_CHANGED &&
			change.GetOwnerPlayerId() == owner.playerID &&
			bytes.Equal(change.GetVisitId(), visitID) &&
			sameVersion(change.GetOwnerStateVersion(), matured.GetStateVersion()) &&
			len(change.GetPlotUpserts()) == 1 &&
			change.GetPlotUpserts()[0].GetPlotState() == plotv1.PlotState_MATURE
	}, "visitor maturity push")
	if maturedVisitor == nil {
		t.Fatal("visitor maturity push missing")
	}
	return maturedVisitor.GetFriendFarmChangedPush().GetPlotUpserts()[0]
}

func assertLivePestGameplay(
	t *testing.T,
	owner, source, catcher friendE2EPlayer,
	sourceVisit, catcherVisit *wsv1.EnterFriendFarmResponse,
) {
	t.Helper()
	apply := func(request *wsv1.WsEnvelope) {
		request.Payload = &wsv1.WsEnvelope_ApplyPestToFriendRequest{
			ApplyPestToFriendRequest: &wsv1.ApplyPestToFriendRequest{
				OwnerPlayerId: owner.playerID, VisitId: sourceVisit.GetVisitId(),
				PlotId: 1, PestId: 1,
			},
		}
	}
	applyID := newUUID(t)
	applied, sourceObserved := friendRequestWithIDObserving(
		t, source, applyID, wsv1.Action_APPLY_PEST_TO_FRIEND, apply,
	)
	if plot := applied.GetApplyPestToFriendResponse().GetOwnerPlot(); plot == nil ||
		!plot.GetPestActive() {
		t.Fatalf("apply pest response did not activate pest: %+v", applied)
	}
	ownerApplied := matchingPush(t, owner, nil, privatePestPushMatcher(
		reasonv1.StateChangeReason_APPLY_PEST_TO_FRIEND, applyID, true,
	), "owner apply-pest push")
	assertPrivatePestPush(t, ownerApplied, owner.playerID, source.playerID, true)
	assertPublicPestPush(t, matchingPush(t, source, sourceObserved,
		publicPestPushMatcher(owner.playerID, sourceVisit.GetVisitId(),
			ownerApplied.GetStateVersion(), true),
		"source apply-pest push"), source, owner.playerID, sourceVisit.GetVisitId(),
		ownerApplied.GetStateVersion(), true)
	assertPublicPestPush(t, matchingPush(t, catcher, nil,
		publicPestPushMatcher(owner.playerID, catcherVisit.GetVisitId(),
			ownerApplied.GetStateVersion(), true),
		"catcher apply-pest push"), catcher, owner.playerID, catcherVisit.GetVisitId(),
		ownerApplied.GetStateVersion(), true)

	replayed := friendRequestWithID(
		t, source, applyID, wsv1.Action_APPLY_PEST_TO_FRIEND, apply,
	)
	if !replayed.GetReplayed() ||
		!proto.Equal(applied.GetApplyPestToFriendResponse(),
			replayed.GetApplyPestToFriendResponse()) {
		t.Fatalf("apply pest replay mismatch first=%+v replay=%+v", applied, replayed)
	}
	afterReplay := playerSnapshotEnvelope(t, owner)
	if !sameVersion(afterReplay.GetStateVersion(), ownerApplied.GetStateVersion()) ||
		snapshotPlot(afterReplay.GetGetPlayerSnapshotResponse().GetSnapshot(), 1).
			GetPestEffect() == nil {
		t.Fatalf("apply replay mutated owner state: %+v", afterReplay)
	}

	sourceCatchID := newUUID(t)
	sourceCatch, _ := friendRequestWithIDAllowErrorObserving(
		t, source, sourceCatchID, wsv1.Action_CATCH_PEST_FOR_FRIEND,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_CatchPestForFriendRequest{
				CatchPestForFriendRequest: &wsv1.CatchPestForFriendRequest{
					OwnerPlayerId: owner.playerID, VisitId: sourceVisit.GetVisitId(), PlotId: 1,
				},
			}
		})
	if sourceCatch.GetError().GetCode() != wsv1.ErrorCode_PEST_SOURCE_FORBIDDEN {
		t.Fatalf("source visitor catch result=%+v", sourceCatch)
	}
	afterForbidden := playerSnapshotEnvelope(t, owner)
	if !sameVersion(afterForbidden.GetStateVersion(), ownerApplied.GetStateVersion()) ||
		snapshotPlot(afterForbidden.GetGetPlayerSnapshotResponse().GetSnapshot(), 1).
			GetPestEffect() == nil {
		t.Fatalf("source catch changed owner plot/version: %+v", afterForbidden)
	}

	ownerCatchID := newUUID(t)
	ownerCatch, ownerObserved := friendRequestWithIDObserving(
		t, owner, ownerCatchID, wsv1.Action_CATCH_PEST,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_CatchPestRequest{
				CatchPestRequest: &wsv1.CatchPestRequest{PlotId: 1},
			}
		})
	ownerCatchPlots := ownerCatch.GetCatchPestResponse().GetPatch().GetPlotUpserts()
	if len(ownerCatchPlots) != 1 || ownerCatchPlots[0].GetPestEffect() != nil {
		t.Fatalf("owner catch response retained pest effect: %+v", ownerCatch)
	}
	ownerCaught := matchingPush(t, owner, ownerObserved, privatePestPushMatcher(
		reasonv1.StateChangeReason_CATCH_PEST, ownerCatchID, false,
	), "owner catch-pest push")
	assertPrivatePestPush(t, ownerCaught, owner.playerID, source.playerID, false)
	if !sameVersion(ownerCatch.GetStateVersion(), ownerCaught.GetStateVersion()) {
		t.Fatalf("owner catch response/push versions differ response=%+v push=%+v",
			ownerCatch, ownerCaught)
	}
	for _, active := range []struct {
		player  friendE2EPlayer
		visitID []byte
	}{{source, sourceVisit.GetVisitId()}, {catcher, catcherVisit.GetVisitId()}} {
		assertPublicPestPush(t, matchingPush(t, active.player, nil,
			publicPestPushMatcher(owner.playerID, active.visitID,
				ownerCaught.GetStateVersion(), false),
			"visitor owner-catch push"), active.player, owner.playerID, active.visitID,
			ownerCaught.GetStateVersion(), false)
	}

	reapplyID := newUUID(t)
	reapplied, sourceObserved := friendRequestWithIDObserving(
		t, source, reapplyID, wsv1.Action_APPLY_PEST_TO_FRIEND, apply,
	)
	if !reapplied.GetApplyPestToFriendResponse().GetOwnerPlot().GetPestActive() {
		t.Fatalf("reapply pest response=%+v", reapplied)
	}
	ownerReapplied := matchingPush(t, owner, nil, privatePestPushMatcher(
		reasonv1.StateChangeReason_APPLY_PEST_TO_FRIEND, reapplyID, true,
	), "owner reapply-pest push")
	assertPrivatePestPush(t, ownerReapplied, owner.playerID, source.playerID, true)
	assertPublicPestPush(t, matchingPush(t, source, sourceObserved,
		publicPestPushMatcher(owner.playerID, sourceVisit.GetVisitId(),
			ownerReapplied.GetStateVersion(), true),
		"source reapply-pest push"), source, owner.playerID, sourceVisit.GetVisitId(),
		ownerReapplied.GetStateVersion(), true)
	assertPublicPestPush(t, matchingPush(t, catcher, nil,
		publicPestPushMatcher(owner.playerID, catcherVisit.GetVisitId(),
			ownerReapplied.GetStateVersion(), true),
		"catcher reapply-pest push"), catcher, owner.playerID, catcherVisit.GetVisitId(),
		ownerReapplied.GetStateVersion(), true)

	friendCatchID := newUUID(t)
	friendCaught, catcherObserved := friendRequestWithIDObserving(
		t, catcher, friendCatchID, wsv1.Action_CATCH_PEST_FOR_FRIEND,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_CatchPestForFriendRequest{
				CatchPestForFriendRequest: &wsv1.CatchPestForFriendRequest{
					OwnerPlayerId: owner.playerID, VisitId: catcherVisit.GetVisitId(), PlotId: 1,
				},
			}
		})
	if friendCaught.GetCatchPestForFriendResponse().GetOwnerPlot().GetPestActive() {
		t.Fatalf("friend catch response retained pest: %+v", friendCaught)
	}
	ownerFriendCaught := matchingPush(t, owner, nil, privatePestPushMatcher(
		reasonv1.StateChangeReason_CATCH_PEST_FOR_FRIEND, friendCatchID, false,
	), "owner friend-catch push")
	assertPrivatePestPush(t, ownerFriendCaught, owner.playerID, source.playerID, false)
	assertPublicPestPush(t, matchingPush(t, source, nil,
		publicPestPushMatcher(owner.playerID, sourceVisit.GetVisitId(),
			ownerFriendCaught.GetStateVersion(), false),
		"source friend-catch push"), source, owner.playerID, sourceVisit.GetVisitId(),
		ownerFriendCaught.GetStateVersion(), false)
	assertPublicPestPush(t, matchingPush(t, catcher, catcherObserved,
		publicPestPushMatcher(owner.playerID, catcherVisit.GetVisitId(),
			ownerFriendCaught.GetStateVersion(), false),
		"catcher friend-catch push"), catcher, owner.playerID, catcherVisit.GetVisitId(),
		ownerFriendCaught.GetStateVersion(), false)
}

func publicPlot(snapshot *wsv1.FarmVisitSnapshot, plotID uint32) *wsv1.PublicPlotView {
	for _, plot := range snapshot.GetPlots() {
		if plot.GetPlotId() == plotID {
			return plot
		}
	}
	return nil
}

func snapshotPlot(snapshot *wsv1.PlayerSnapshot, plotID uint32) *wsv1.PlotView {
	for _, plot := range snapshot.GetPlots() {
		if plot.GetPlotId() == plotID {
			return plot
		}
	}
	return nil
}

func sameVersion(first, second *wsv1.StateVersion) bool {
	return first != nil && second != nil &&
		first.GetOwnerEpoch() == second.GetOwnerEpoch() &&
		first.GetPlayerSeq() == second.GetPlayerSeq()
}

func privatePestPushMatcher(
	reason reasonv1.StateChangeReason,
	requestID string,
	pestActive bool,
) func(*wsv1.WsEnvelope) bool {
	return func(push *wsv1.WsEnvelope) bool {
		change := push.GetPlayerStateChangedPush()
		if push.GetMessageKind() != wsv1.MessageKind_PUSH ||
			push.GetAction() != wsv1.Action_PLAYER_STATE_CHANGED ||
			change.GetReason() != reason ||
			change.GetCausedByRequestId() != requestID ||
			len(change.GetPatch().GetPlotUpserts()) != 1 {
			return false
		}
		return (change.GetPatch().GetPlotUpserts()[0].GetPestEffect() != nil) == pestActive
	}
}

func publicPestPushMatcher(
	ownerPlayerID uint64,
	visitID []byte,
	version *wsv1.StateVersion,
	pestActive bool,
) func(*wsv1.WsEnvelope) bool {
	return func(push *wsv1.WsEnvelope) bool {
		change := push.GetFriendFarmChangedPush()
		return push.GetMessageKind() == wsv1.MessageKind_PUSH &&
			push.GetAction() == wsv1.Action_FRIEND_FARM_CHANGED &&
			change.GetOwnerPlayerId() == ownerPlayerID &&
			bytes.Equal(change.GetVisitId(), visitID) &&
			sameVersion(change.GetOwnerStateVersion(), version) &&
			len(change.GetPlotUpserts()) == 1 &&
			change.GetPlotUpserts()[0].GetPestActive() == pestActive
	}
}

func assertPrivatePestPush(
	t *testing.T,
	push *wsv1.WsEnvelope,
	ownerPlayerID, sourcePlayerID uint64,
	pestActive bool,
) {
	t.Helper()
	change := push.GetPlayerStateChangedPush()
	if push.GetTargetPlayerId() != ownerPlayerID ||
		push.GetStateVersion() == nil ||
		len(change.GetPatch().GetPlotUpserts()) != 1 {
		t.Fatalf("invalid private pest push: %+v", push)
	}
	effect := change.GetPatch().GetPlotUpserts()[0].GetPestEffect()
	if pestActive && (effect == nil || effect.GetEffectItemId() != 1 ||
		effect.GetSourcePlayerId() != sourcePlayerID) {
		t.Fatalf("private pest push has wrong effect: %+v", push)
	}
	if !pestActive && effect != nil {
		t.Fatalf("private pest push did not clear effect: %+v", push)
	}
}

func assertPublicPestPush(
	t *testing.T,
	push *wsv1.WsEnvelope,
	visitor friendE2EPlayer,
	ownerPlayerID uint64,
	visitID []byte,
	version *wsv1.StateVersion,
	pestActive bool,
) {
	t.Helper()
	change := push.GetFriendFarmChangedPush()
	if push.GetTargetPlayerId() != visitor.playerID ||
		change.GetOwnerPlayerId() != ownerPlayerID ||
		!bytes.Equal(change.GetVisitId(), visitID) ||
		!sameVersion(change.GetOwnerStateVersion(), version) ||
		len(change.GetPlotUpserts()) != 1 ||
		change.GetPlotUpserts()[0].GetPestActive() != pestActive {
		t.Fatalf("invalid public pest push: %+v", push)
	}
}

func matchingPush(
	t *testing.T,
	player friendE2EPlayer,
	observed []*wsv1.WsEnvelope,
	match func(*wsv1.WsEnvelope) bool,
	description string,
) *wsv1.WsEnvelope {
	t.Helper()
	for _, push := range observed {
		if match(push) {
			return push
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		push := readEnvelopeWithTimeout(t, player.conn, time.Until(deadline))
		if match(push) {
			return push
		}
	}
	t.Fatalf("%s not received for player %d", description, player.playerID)
	return nil
}

func assertPlayerHasItem(
	t *testing.T,
	player friendE2EPlayer,
	itemID, minimumQuantity uint32,
) {
	t.Helper()
	assertSnapshotHasItem(t, player.playerID, playerSnapshot(t, player), itemID, minimumQuantity)
}

func playerSnapshot(t *testing.T, player friendE2EPlayer) *wsv1.PlayerSnapshot {
	t.Helper()
	return playerSnapshotEnvelope(t, player).GetGetPlayerSnapshotResponse().GetSnapshot()
}

func playerSnapshotEnvelope(t *testing.T, player friendE2EPlayer) *wsv1.WsEnvelope {
	t.Helper()
	return friendRequest(t, player, wsv1.Action_GET_PLAYER_SNAPSHOT,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_GetPlayerSnapshotRequest{
				GetPlayerSnapshotRequest: &wsv1.GetPlayerSnapshotRequest{},
			}
		})
}

func assertSnapshotHasItem(
	t *testing.T,
	playerID uint64,
	snapshot *wsv1.PlayerSnapshot,
	itemID, minimumQuantity uint32,
) {
	t.Helper()
	for _, item := range snapshot.GetInventory() {
		if item.GetItemId() == itemID && item.GetQuantity() >= minimumQuantity {
			return
		}
	}
	t.Fatalf("player %d inventory lacks item %d x%d: %+v",
		playerID, itemID, minimumQuantity, snapshot.GetInventory())
}

func registerFriendE2EPlayer(t *testing.T, loginURL string) friendE2EPlayer {
	t.Helper()
	baseURL, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	accountName := uniqueAccountName(t)
	register := &httpv1.RegisterResponse{}
	doProto(t, client, http.MethodPost, loginURL+"/v1/auth/register",
		&httpv1.RegisterRequest{AccountName: accountName, Password: e2ePassword},
		getCSRF(t, client, loginURL), http.StatusCreated, register)
	session := register.GetSession()
	if session.GetPlayerId() == 0 {
		t.Fatal("registration returned no player")
	}
	csrf := getCSRF(t, client, loginURL)
	bootstrap := &httpv1.ClientBootstrapResponse{}
	doProto(t, client, http.MethodGet, loginURL+"/v1/bootstrap", nil, "", http.StatusOK, bootstrap)
	gateway := bootstrap.GetGateways()[0]
	ticket := &httpv1.WsTicketResponse{}
	doProto(t, client, http.MethodPost, loginURL+"/v1/ws-tickets",
		&httpv1.WsTicketRequest{TicketRequestId: newUUID(t), GatewayId: gateway.GetGatewayId()},
		csrf, http.StatusCreated, ticket)
	if cookieValue(jar.Cookies(baseURL), "cf_session_dev") == "" {
		t.Fatal("registration did not establish a Session")
	}
	conn := dialWebSocket(t, gateway.GetWebsocketUrl())
	writeEnvelope(t, conn, &wsv1.WsEnvelope{
		ProtocolVersion: 1,
		MessageKind:     wsv1.MessageKind_REQUEST,
		Action:          wsv1.Action_AUTH,
		RequestId:       newUUID(t),
		Payload: &wsv1.WsEnvelope_AuthRequest{
			AuthRequest: &wsv1.AuthRequest{WsTicket: ticket.GetWsTicket()},
		},
	})
	auth := readEnvelope(t, conn)
	if auth.GetError() != nil || auth.GetAuthResponse().GetPlayerId() != session.GetPlayerId() {
		conn.CloseNow()
		t.Fatalf("AUTH failed: %+v", auth)
	}
	return friendE2EPlayer{accountName: accountName, playerID: session.GetPlayerId(), conn: conn}
}

func loginFriendE2EPlayer(
	t *testing.T,
	loginURL, accountName string,
) friendE2EPlayer {
	t.Helper()
	baseURL, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	login := &httpv1.LoginResponse{}
	doProto(t, client, http.MethodPost, loginURL+"/v1/auth/login",
		&httpv1.LoginRequest{AccountName: accountName, Password: e2ePassword},
		getCSRF(t, client, loginURL), http.StatusOK, login)
	session := login.GetSession()
	if session.GetPlayerId() == 0 {
		t.Fatal("login returned no player")
	}
	csrf := getCSRF(t, client, loginURL)
	bootstrap := &httpv1.ClientBootstrapResponse{}
	doProto(t, client, http.MethodGet, loginURL+"/v1/bootstrap", nil, "", http.StatusOK, bootstrap)
	gateway := bootstrap.GetGateways()[0]
	ticket := &httpv1.WsTicketResponse{}
	doProto(t, client, http.MethodPost, loginURL+"/v1/ws-tickets",
		&httpv1.WsTicketRequest{TicketRequestId: newUUID(t), GatewayId: gateway.GetGatewayId()},
		csrf, http.StatusCreated, ticket)
	if cookieValue(jar.Cookies(baseURL), "cf_session_dev") == "" {
		t.Fatal("login did not establish a Session")
	}
	conn := dialWebSocket(t, gateway.GetWebsocketUrl())
	writeEnvelope(t, conn, &wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_AUTH, RequestId: newUUID(t),
		Payload: &wsv1.WsEnvelope_AuthRequest{
			AuthRequest: &wsv1.AuthRequest{WsTicket: ticket.GetWsTicket()},
		},
	})
	auth := readEnvelope(t, conn)
	if auth.GetError() != nil || auth.GetAuthResponse().GetPlayerId() != session.GetPlayerId() {
		conn.CloseNow()
		t.Fatalf("AUTH after login failed: %+v", auth)
	}
	return friendE2EPlayer{
		accountName: accountName, playerID: session.GetPlayerId(), conn: conn,
	}
}

func friendRequest(
	t *testing.T,
	player friendE2EPlayer,
	action wsv1.Action,
	setPayload func(*wsv1.WsEnvelope),
) *wsv1.WsEnvelope {
	t.Helper()
	return friendRequestWithID(t, player, newUUID(t), action, setPayload)
}

func friendRequestWithID(
	t *testing.T,
	player friendE2EPlayer,
	requestID string,
	action wsv1.Action,
	setPayload func(*wsv1.WsEnvelope),
) *wsv1.WsEnvelope {
	t.Helper()
	response, _ := friendRequestWithIDObserving(
		t, player, requestID, action, setPayload,
	)
	return response
}

func friendRequestWithIDObserving(
	t *testing.T,
	player friendE2EPlayer,
	requestID string,
	action wsv1.Action,
	setPayload func(*wsv1.WsEnvelope),
) (*wsv1.WsEnvelope, []*wsv1.WsEnvelope) {
	t.Helper()
	response, pushes := friendRequestWithIDAllowErrorObserving(
		t, player, requestID, action, setPayload,
	)
	if response.GetError() != nil {
		t.Fatalf("friend action %s failed: %+v", action, response)
	}
	return response, pushes
}

func friendRequestWithIDAllowErrorObserving(
	t *testing.T,
	player friendE2EPlayer,
	requestID string,
	action wsv1.Action,
	setPayload func(*wsv1.WsEnvelope),
) (*wsv1.WsEnvelope, []*wsv1.WsEnvelope) {
	t.Helper()
	request := &wsv1.WsEnvelope{
		ProtocolVersion: 1,
		MessageKind:     wsv1.MessageKind_REQUEST,
		Action:          action,
		RequestId:       requestID,
		TargetPlayerId:  player.playerID,
	}
	setPayload(request)
	writeEnvelope(t, player.conn, request)
	var pushes []*wsv1.WsEnvelope
	for {
		response := readEnvelope(t, player.conn)
		if response.GetMessageKind() == wsv1.MessageKind_PUSH {
			pushes = append(pushes, response)
			continue
		}
		if response.GetRequestId() != requestID || response.GetAction() != action ||
			response.GetMessageKind() != wsv1.MessageKind_RESPONSE {
			t.Fatalf("friend action %s failed: %+v", action, response)
		}
		return response, pushes
	}
}

func assertFriendList(t *testing.T, player, want friendE2EPlayer) {
	t.Helper()
	response := friendRequest(t, player, wsv1.Action_LIST_FRIENDS,
		func(request *wsv1.WsEnvelope) {
			request.Payload = &wsv1.WsEnvelope_ListFriendsRequest{
				ListFriendsRequest: &wsv1.ListFriendsRequest{},
			}
		})
	friends := response.GetListFriendsResponse().GetFriends()
	for _, friend := range friends {
		if friend.GetPlayerId() == want.playerID &&
			friend.GetAccountName() == want.accountName {
			return
		}
	}
	t.Fatalf("player %d friend list lacks %d/%s: %+v",
		player.playerID, want.playerID, want.accountName, friends)
}

func routeOwner(t *testing.T, playerID uint64) string {
	t.Helper()
	endpoint := envOr("COORDINATOR_URL", "http://127.0.0.1:8083") +
		"/internal/v1/routes/" + formatShard(playerID)
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var route struct {
		OwnerZoneID string `json:"owner_zone_id"`
		Routable    bool   `json:"routable"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&route) != nil ||
		!route.Routable || route.OwnerZoneID == "" {
		t.Fatalf("invalid route for player %d", playerID)
	}
	return route.OwnerZoneID
}

func formatShard(playerID uint64) string {
	return fmt.Sprint(routing.ShardForPlayer(playerID))
}

func assertPersistedFriendRelation(t *testing.T, first, second uint64) {
	t.Helper()
	db, err := sql.Open("mysql", os.Getenv("MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	low, high := first, second
	if low > high {
		low, high = high, low
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friend_relations
		WHERE player_low_id = ? AND player_high_id = ? AND status = 'ACTIVE'`,
		low, high).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted relation count=%d", count)
	}
}
