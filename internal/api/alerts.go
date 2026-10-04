// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"

	"github.com/n1k0droid/zweep/internal/store"
)

// Forced close of alerts: an alarm whose recovery never arrived stays active on the devices.
// Closing it sends a recovery to every recipient; Zabbix is never touched.

var errAlertNotOpen = errorf(http.StatusNotFound, 40403, "alert_not_open", "the alert is not open")

// handleAlertClose closes an alert from the app; the user needs the can_close permission
func (s *Service) handleAlertClose(w http.ResponseWriter, r *http.Request) {
	dev := deviceOf(r)
	var in struct {
		SID string `json:"sid"`
	}
	if err := readJSON(r, &in, 1<<10); err != nil || in.SID == "" {
		writeError(w, errBadJSON)
		return
	}
	perimeter, err := s.perimeterOf(r, dev.UserID)
	if err != nil {
		storeError(w, err)
		return
	}
	if perimeter == nil || !perimeter.CanClose {
		writeError(w, errorf(http.StatusForbidden, 40312, "close_not_allowed", "the administrator did not allow closing alerts for this user"))
		return
	}
	// An operator closes only an alert they still see open
	open, err := s.st.HasOpenAlert(r.Context(), dev.UserID, in.SID)
	if err != nil {
		storeError(w, err)
		return
	}
	if !open {
		writeError(w, errAlertNotOpen)
		return
	}
	users, status, err := s.closeAlert(w, r, in.SID, dev.Username)
	if err != nil {
		return
	}
	s.audit(r.Context(), store.AuditEntry{ActorType: store.ActorDevice, Actor: dev.ID.String(), Action: "alert.close", Target: in.SID,
		IP: ipPtr(requestInfo(r).IP), Details: map[string]any{"reason": store.CloseManual, "by": dev.Username, "users": users, "zabbix_status": status}})
	writeJSON(w, http.StatusOK, map[string]any{"sid": in.SID, "users": len(users)})
}

// closeAlert closes for every recipient; it writes the error response itself
func (s *Service) closeAlert(w http.ResponseWriter, r *http.Request, sid, by string) (users []string, status string, err error) {
	a, err := s.st.OpenAlert(r.Context(), sid)
	if err == nil {
		status = a.ProblemStatus
		users, err = s.st.ForceClose(r.Context(), sid, store.CloseInfo{By: by, Reason: store.CloseManual, Status: status})
	}
	switch {
	case errors.Is(err, store.ErrAlertNotOpen):
		writeError(w, errAlertNotOpen)
	case err != nil:
		storeError(w, err)
	}
	return users, status, err
}

// adminOpenAlerts lists the alerts still open on the devices, with the Zabbix status
func (s *Service) adminOpenAlerts(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.OpenAlerts(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// adminCloseAlert closes an alert for every recipient
func (s *Service) adminCloseAlert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SID string `json:"sid"`
	}
	if err := readJSON(r, &in, 1<<10); err != nil || in.SID == "" {
		writeError(w, errBadJSON)
		return
	}
	users, status, err := s.closeAlert(w, r, in.SID, requestInfo(r).Admin)
	if err != nil {
		return
	}
	s.adminLog(r, "alert.close", in.SID, map[string]any{"reason": store.CloseManual, "users": users, "zabbix_status": status})
	writeJSON(w, http.StatusOK, map[string]any{"sid": in.SID, "users": len(users)})
}
