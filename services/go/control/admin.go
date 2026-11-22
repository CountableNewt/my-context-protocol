package control

import (
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"net/http"
	"strconv"
	"strings"
)

func (s *Service) admin(r *http.Request, actor *identity.Account, path []string) (any, int, error) {
	ctx := r.Context()
	if len(path) == 1 && path[0] == "metrics" && r.Method == "GET" {
		v, e := s.one(ctx, "SELECT (SELECT COUNT(*) FROM accounts) AS total_users,(SELECT COUNT(*) FROM projects) AS total_projects,(SELECT COUNT(*) FROM request_logs) AS total_mcp_calls")
		return v, 200, e
	}
	if len(path) == 1 && path[0] == "privileged-accounts" && r.Method == "GET" {
		rows, e := s.query(ctx, "SELECT id AS account_id,login AS github_login,is_admin,admin_granted_at FROM accounts WHERE is_admin=1 ORDER BY login,id")
		for _, v := range rows {
			v["is_admin"] = boolean(v["is_admin"])
			v["admin_granted_at"] = date(v["admin_granted_at"])
			v["paywall_bypass"] = false
			v["paywall_bypass_granted_at"] = nil
		}
		return rows, 200, e
	}
	if len(path) == 1 && path[0] == "lookup" && r.Method == "POST" {
		var body struct {
			Login *string `json:"github_login"`
			ID    *string `json:"github_id"`
			Email *string `json:"email"`
		}
		if e := decode(r, &body); e != nil {
			return nil, 0, e
		}
		field, value, count := "", "", 0
		for key, v := range map[string]*string{"login": body.Login, "github_id": body.ID, "email": body.Email} {
			if v != nil && strings.TrimSpace(*v) != "" {
				field = key
				value = strings.ToLower(strings.TrimSpace(*v))
				count++
			}
		}
		if count != 1 {
			return nil, 0, bad("Provide exactly one of github_login, github_id, or email")
		}
		var arg any = value
		if field == "github_id" {
			id, e := strconv.ParseInt(value, 10, 64)
			if e != nil {
				return nil, 0, bad("Invalid github_id")
			}
			arg = id
		}
		sql := "SELECT id AS account_id,is_admin FROM accounts WHERE " + field + "=?"
		if field != "github_id" {
			sql = "SELECT id AS account_id,is_admin FROM accounts WHERE LOWER(" + field + ")=?"
		}
		v, e := s.one(ctx, sql, arg)
		if e != nil {
			return nil, 0, e
		}
		if v == nil {
			return nil, 0, missing("Account not found")
		}
		v["is_admin"] = boolean(v["is_admin"])
		v["paywall_bypass"] = false
		return v, 200, nil
	}
	if len(path) == 1 && path[0] == "account-flags" && r.Method == "POST" {
		var body struct {
			ID    string `json:"account_id"`
			Admin *bool  `json:"is_admin"`
		}
		if e := decode(r, &body); e != nil {
			return nil, 0, e
		}
		if body.Admin == nil {
			return nil, 0, bad("Set is_admin; payment bypass controls have been removed")
		}
		id, e := uuid(body.ID)
		if e != nil {
			return nil, 0, bad("Invalid account ID")
		}
		if id == actor.ID && !*body.Admin {
			return nil, 0, bad("Cannot remove your own admin access")
		}
		var granted any
		if *body.Admin {
			granted = s.now().Unix()
		}
		result, e := s.exec(ctx, "UPDATE accounts SET is_admin=?,admin_granted_at=? WHERE id=?", *body.Admin, granted, id)
		if e != nil {
			return nil, 0, e
		}
		if result.AffectedRowCount != 1 {
			return nil, 0, missing("Account not found")
		}
		return row{"account_id": id, "is_admin": *body.Admin, "paywall_bypass": false}, 200, nil
	}
	if len(path) == 1 && path[0] == "timeseries" && r.Method == "GET" {
		return s.timeseries(r, "", "", true)
	}
	if len(path) == 2 && path[0] == "analytics" && path[1] == "rollup-refresh" && r.Method == "POST" {
		return nil, 204, s.refreshRollup(ctx)
	}
	return nil, 0, missing("Not found")
}
