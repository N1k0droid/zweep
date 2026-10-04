// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/store"
)

func (s *Service) registerAdminUsers() {
	s.mux.HandleFunc("GET /v1/admin/users", s.adminListUsers)
	s.mux.HandleFunc("POST /v1/admin/users", s.adminCreateUser)
	s.mux.HandleFunc("GET /v1/admin/users/{username}", s.adminGetUser)
	s.mux.HandleFunc("PUT /v1/admin/users/{username}", s.adminUpdateUser)
	s.mux.HandleFunc("PUT /v1/admin/users/{username}/password", s.adminSetPassword)
	s.mux.HandleFunc("DELETE /v1/admin/users/{username}", s.adminDeleteUser)
}

type userJSON struct {
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	Password    *string `json:"password,omitempty"`
	Disabled    bool    `json:"disabled"`
}

var (
	errInvalidUsername = errorf(http.StatusBadRequest, 40020, "invalid_username", "username: letters, digits, '.', '_', '@' or '-' (max 64)")
	errInvalidRole     = errorf(http.StatusBadRequest, 40021, "invalid_role", "role must be admin or operator")
	errInvalidName     = errorf(http.StatusBadRequest, 40022, "invalid_display_name", "display_name: max 128 characters")
	errWeakPassword    = errorf(http.StatusUnprocessableEntity, 42220, "weak_password", auth.ErrWeakPassword.Error())
	errLastAdmin       = errorf(http.StatusConflict, 40910, "last_admin", "the last active admin cannot be removed, demoted or disabled")
	errPrimaryAdmin    = errorf(http.StatusConflict, 40911, "primary_admin", "the primary admin cannot be deleted, disabled or demoted")
	errUserExists      = errorf(http.StatusConflict, 40911, "user_exists", "a user with this username already exists")
)

func validUserFields(in *userJSON) *apiError {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(in.DisplayName) > 128 {
		return errInvalidName
	}
	if !store.ValidRole(in.Role) {
		return errInvalidRole
	}
	return nil
}

// hashPassword applies the policy; dashboard accounts always need a password, an operator may enroll by QR only
func (s *Service) hashPassword(r *http.Request, username, role string, password *string) (string, *apiError) {
	if password == nil || *password == "" {
		if store.DashboardRole(role) {
			return "", errWeakPassword
		}
		return "", nil
	}
	if err := auth.CheckPolicy(username, *password); err != nil {
		return "", errWeakPassword
	}
	h, err := auth.Hash(r.Context(), *password)
	if err != nil {
		return "", errUnavailable
	}
	return h, nil
}

func (s *Service) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.Users(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Service) adminGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.UserByName(r.Context(), r.PathValue("username"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Service) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userJSON
	if err := readJSON(r, &in, 8<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	if !store.ValidUsername(in.Username) {
		writeError(w, errInvalidUsername)
		return
	}
	if e := validUserFields(&in); e != nil {
		writeError(w, e)
		return
	}
	hash, e := s.hashPassword(r, in.Username, in.Role, in.Password)
	if e != nil {
		writeError(w, e)
		return
	}
	u := &store.User{Username: in.Username, DisplayName: in.DisplayName, Role: in.Role, PasswordHash: hash}
	if err := s.st.CreateUser(r.Context(), u, requestInfo(r).Admin); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, errUserExists)
			return
		}
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.user.create", u.Username, map[string]any{"role": u.Role, "password_set": hash != ""})
	created, err := s.st.UserByName(r.Context(), u.Username)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Service) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in userJSON
	if err := readJSON(r, &in, 8<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	if in.Password != nil {
		writeError(w, errorf(http.StatusBadRequest, 40023, "use_password_endpoint", "change the password with PUT /v1/admin/users/{username}/password"))
		return
	}
	if e := validUserFields(&in); e != nil {
		writeError(w, e)
		return
	}
	name := r.PathValue("username")
	current, err := s.st.UserByName(r.Context(), name)
	if err != nil {
		storeError(w, err)
		return
	}
	if store.DashboardRole(in.Role) && !current.HasPassword {
		writeError(w, errWeakPassword) // a dashboard account must be able to log in: set a password first
		return
	}
	u, revoked, err := s.st.UpdateUser(r.Context(), name, in.DisplayName, in.Role, in.Disabled, requestInfo(r).Admin)
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, errLastAdmin)
		return
	} else if errors.Is(err, store.ErrPrimaryAdmin) {
		writeError(w, errPrimaryAdmin)
		return
	} else if err != nil {
		storeError(w, err)
		return
	}
	for _, id := range revoked {
		s.hub.Kick(id, delivery.NoticeTokenRevoked, "account disabled by the administrator")
	}
	s.adminLog(r, "admin.user.update", u.Username, map[string]any{"role": u.Role, "disabled": u.Disabled, "devices_revoked": len(revoked)})
	writeJSON(w, http.StatusOK, u)
}

func (s *Service) adminSetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password *string `json:"password"`
	}
	if err := readJSON(r, &in, 4<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	u, err := s.st.UserByName(r.Context(), r.PathValue("username"))
	if err != nil {
		storeError(w, err)
		return
	}
	hash, e := s.hashPassword(r, u.Username, u.Role, in.Password)
	if e != nil {
		writeError(w, e)
		return
	}
	if err := s.st.SetPasswordHash(r.Context(), u.Username, hash, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.user.password", u.Username, map[string]any{"password_set": hash != ""})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	revoked, err := s.st.DeleteUser(r.Context(), name)
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, errLastAdmin)
		return
	} else if errors.Is(err, store.ErrPrimaryAdmin) {
		writeError(w, errPrimaryAdmin)
		return
	} else if err != nil {
		storeError(w, err)
		return
	}
	for _, id := range revoked {
		s.hub.Kick(id, delivery.NoticeTokenRevoked, "account deleted by the administrator")
	}
	s.adminLog(r, "admin.user.delete", name, map[string]any{"devices_revoked": len(revoked)})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
