// This code is available on the terms of the project LICENSE.md file,
// also available online at https://blueoakcouncil.org/license/1.0.0.

package admin

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"decred.org/dcrdex/dex/encode"
	"decred.org/dcrdex/server/account"
	"github.com/go-chi/chi"
)

const (
	pongStr = "pong"
)

// writeJSON marshals the provided interface and writes the bytes to the
// ResponseWriter. The response code is assumed to be StatusOK.
func writeJSON(w http.ResponseWriter, thing interface{}) {
	writeJSONWithStatus(w, thing, http.StatusOK)
}

// writeJSON marshals the provided interface and writes the bytes to the
// ResponseWriter with the specified response code.
func writeJSONWithStatus(w http.ResponseWriter, thing interface{}, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "    ")
	if err := encoder.Encode(thing); err != nil {
		log.Errorf("JSON encode error: %v", err)
	}
}

// apiPing is the handler for the '/ping' API request.
func (_ *Server) apiPing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, pongStr)
}

// apiConfig is the handler for the '/config' API request.
func (s *Server) apiConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.core.ConfigMsg())
}

func (s *Server) apiMarkets(w http.ResponseWriter, r *http.Request) {
	statuses := s.core.MarketStatuses()
	mktStatuses := make(map[string]*MarketStatus)
	for name, status := range statuses {
		mktStatus := &MarketStatus{
			// Name is empty since the key is the name.
			Running:       status.Running,
			EpochDuration: status.EpochDuration,
			ActiveEpoch:   status.ActiveEpoch,
			StartEpoch:    status.StartEpoch,
			SuspendEpoch:  status.SuspendEpoch,
		}
		if status.SuspendEpoch != 0 {
			persist := status.PersistBook
			mktStatus.PersistBook = &persist
		}
		mktStatuses[name] = mktStatus
	}

	writeJSON(w, mktStatuses)
}

// apiMarketInfo is the handler for the '/market/{marketName}' API request.
func (s *Server) apiMarketInfo(w http.ResponseWriter, r *http.Request) {
	mkt := strings.ToLower(chi.URLParam(r, marketNameKey))
	status := s.core.MarketStatus(mkt)
	if status == nil {
		http.Error(w, fmt.Sprintf("unknown market %q", mkt), http.StatusBadRequest)
		return
	}

	mktStatus := &MarketStatus{
		Name:          mkt,
		Running:       status.Running,
		EpochDuration: status.EpochDuration,
		ActiveEpoch:   status.ActiveEpoch,
		StartEpoch:    status.ActiveEpoch,
		SuspendEpoch:  status.SuspendEpoch,
	}
	if status.SuspendEpoch != 0 {
		persist := status.PersistBook
		mktStatus.PersistBook = &persist
	}
	writeJSON(w, mktStatus)
}

// hander for route '/market/{marketName}/suspend?t=EPOCH-MS&persist=BOOL'
func (s *Server) apiSuspend(w http.ResponseWriter, r *http.Request) {
	// Ensure the market exists and is running.
	mkt := strings.ToLower(chi.URLParam(r, marketNameKey))
	found, running := s.core.MarketRunning(mkt)
	if !found {
		http.Error(w, fmt.Sprintf("unknown market %q", mkt), http.StatusBadRequest)
		return
	}
	if !running {
		http.Error(w, fmt.Sprintf("market %q not running", mkt), http.StatusBadRequest)
		return
	}

	// Validate the suspend time provided in the "t" query. If not specified,
	// the zero time.Time is used to indicate ASAP.
	var suspTime time.Time
	if tSuspendStr := r.URL.Query().Get("t"); tSuspendStr != "" {
		suspTimeMs, err := strconv.ParseInt(tSuspendStr, 10, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid suspend time %q: %v", tSuspendStr, err), http.StatusBadRequest)
			return
		}

		suspTime = encode.UnixTimeMilli(suspTimeMs)
		if time.Until(suspTime) < 0 {
			http.Error(w, fmt.Sprintf("specified market suspend time is in the past: %v", suspTime),
				http.StatusBadRequest)
			return
		}
	}

	// Validate the persist book flag provided in the "persist" query. If not
	// specified, persist the books, do not purge.
	persistBook := true
	if persistBookStr := r.URL.Query().Get("persist"); persistBookStr != "" {
		var err error
		persistBook, err = strconv.ParseBool(persistBookStr)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid persist book boolean %q: %v", persistBookStr, err), http.StatusBadRequest)
			return
		}
	}

	suspEpoch := s.core.SuspendMarket(mkt, suspTime, persistBook)
	if suspEpoch == nil {
		// Should not happen.
		http.Error(w, "failed to suspend market "+mkt, http.StatusInternalServerError)
		return
	}

	writeJSON(w, &SuspendResult{
		Market:      mkt,
		FinalEpoch:  suspEpoch.Idx,
		SuspendTime: APITime{suspEpoch.End},
	})
}

// apiAccounts is the handler for the '/accounts' API request.
func (s *Server) apiAccounts(w http.ResponseWriter, _ *http.Request) {
	accts, err := s.core.Accounts()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to retrieve accounts: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, accts)
}

// apiAccountInfo is the handler for the '/account/{account id}' API request.
func (s *Server) apiAccountInfo(w http.ResponseWriter, r *http.Request) {
	acctIDStr := chi.URLParam(r, accountIDKey)
	acctIDSlice, err := hex.DecodeString(acctIDStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not decode accout id: %v", err), http.StatusBadRequest)
		return
	}
	if len(acctIDSlice) != account.HashSize {
		http.Error(w, "account id has incorrect length", http.StatusBadRequest)
		return
	}
	var acctID account.AccountID
	copy(acctID[:], acctIDSlice)
	acctInfo, err := s.core.AccountInfo(acctID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to retrieve account: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, acctInfo)
}

// apiBan is the handler for the '/account/{accountID}/ban?rule=RULE' API request.
func (s *Server) apiBan(w http.ResponseWriter, r *http.Request) {
	acctIDStr := chi.URLParam(r, accountIDKey)
	acctIDSlice, err := hex.DecodeString(acctIDStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not decode accout id: %v", err), http.StatusBadRequest)
		return
	}
	if len(acctIDSlice) != account.HashSize {
		http.Error(w, "account id has incorrect length", http.StatusBadRequest)
		return
	}
	ruleStr := r.URL.Query().Get(ruleToken)
	if ruleStr == "" {
		http.Error(w, "rule not specified", http.StatusBadRequest)
		return
	}
	ruleInt, err := strconv.Atoi(ruleStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad rule: %v", err), http.StatusBadRequest)
		return
	}
	if ruleInt < 1 || ruleInt >= int(account.MaxRule) {
		http.Error(w, "bad rule: not known or not punishable", http.StatusBadRequest)
		return
	}
	var acctID account.AccountID
	copy(acctID[:], acctIDSlice)
	if err := s.core.Penalize(acctID, account.Rule(ruleInt)); err != nil {
		http.Error(w, fmt.Sprintf("failed to ban account: %v", err), http.StatusInternalServerError)
		return
	}
	res := BanResult{
		AccountID:  acctIDStr,
		BrokenRule: byte(ruleInt),
		BanTime:    APITime{time.Now()},
	}
	writeJSON(w, res)
}
