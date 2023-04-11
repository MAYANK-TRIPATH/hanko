package handler

import (
	"bytes"
	"encoding/json"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/suite"
	auditlog "github.com/teamhanko/hanko/backend/audit_log"
	"github.com/teamhanko/hanko/backend/config"
	"github.com/teamhanko/hanko/backend/dto"
	"github.com/teamhanko/hanko/backend/persistence"
	"github.com/teamhanko/hanko/backend/persistence/models"
	"github.com/teamhanko/hanko/backend/test"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenSuite(t *testing.T) {
	suite.Run(t, new(tokenSuite))
}

type tokenSuite struct {
	suite.Suite
	storage persistence.Storage
	db      *test.TestDB
}

func (s *tokenSuite) SetupSuite() {
	if testing.Short() {
		return
	}
	dialect := "postgres"
	db, err := test.StartDB("token_test", dialect)
	s.NoError(err)
	storage, err := persistence.New(config.Database{
		Url: db.DatabaseUrl,
	})
	s.NoError(err)

	s.storage = storage
	s.db = db
}

func (s *tokenSuite) SetupTest() {
	if s.db != nil {
		err := s.storage.MigrateUp()
		s.NoError(err)
	}
}

func (s *tokenSuite) TearDownTest() {
	if s.db != nil {
		err := s.storage.MigrateDown(-1)
		s.NoError(err)
	}
}

func (s *tokenSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(test.PurgeDB(s.db))
	}
}

func (s *tokenSuite) TestToken_Validate() {
	if testing.Short() {
		s.T().Skip("skipping test in short mode.")
	}

	err := test.LoadFixtures(s.db.DbCon, s.db.Dialect, "../test/fixtures/token")

	e := echo.New()
	e.Validator = dto.NewCustomValidator()

	// must create and insert a valid token manually instead of using fixtures, because token
	// validation is time sensitive (expiration is checked relative to current time)
	uId := uuid.FromStringOrNil("b5dd5267-b462-48be-b70d-bcd6f1bbe7a5")
	token, err := models.NewToken(uId)
	s.NoError(err)
	err = s.storage.GetTokenPersister().Create(*token)
	s.NoError(err)

	body := TokenValidationBody{Value: token.Value}
	bodyJson, err := json.Marshal(body)
	s.NoError(err)

	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewReader(bodyJson))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cfg := s.setupConfig()
	auditLogger := auditlog.NewLogger(s.storage, cfg.AuditLog)
	handler := NewTokenHandler(cfg, s.storage, sessionManager{}, auditLogger)
	if s.NoError(handler.Validate(c)) {
		t, err := s.storage.GetTokenPersister().GetByValue(token.Value)
		s.NoError(err)
		s.Nil(t)

		setCookie := rec.Header().Get("Set-Cookie")
		s.True(strings.HasPrefix(setCookie, "hanko"))

		tokenHeader := rec.Header().Get("X-Auth-Token")
		s.NotEmpty(tokenHeader)

		logs, err := s.storage.GetAuditLogPersister().List(0, 0, nil, nil, []string{"token_exchange_succeeded"}, "b5dd5267-b462-48be-b70d-bcd6f1bbe7a5", "", "", "")
		s.Len(logs, 1)
	}
}

func (s *tokenSuite) TestToken_Validate_ExpiredToken() {
	if testing.Short() {
		s.T().Skip("skipping test in short mode.")
	}

	err := test.LoadFixtures(s.db.DbCon, s.db.Dialect, "../test/fixtures/token")

	e := echo.New()
	e.Validator = dto.NewCustomValidator()

	expiredTokenValue := "Trkauhl3q7XVxw5JcDH80lTe1KxzydIw0OcizH7umWk="
	body := TokenValidationBody{Value: expiredTokenValue}
	bodyJson, err := json.Marshal(body)
	s.NoError(err)

	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewReader(bodyJson))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cfg := s.setupConfig()
	auditLogger := auditlog.NewLogger(s.storage, cfg.AuditLog)
	handler := NewTokenHandler(cfg, s.storage, sessionManager{}, auditLogger)
	err = handler.Validate(c)
	if s.Error(err) {
		herr, ok := err.(*dto.HTTPError)
		s.True(ok)
		s.Equal(http.StatusUnprocessableEntity, herr.Code)
		s.Equal("token has expired", herr.Message)

		logs, lerr := s.storage.GetAuditLogPersister().List(0, 0, nil, nil, []string{"token_exchange_failed"}, "", "", "", "")
		s.NoError(lerr)
		s.Len(logs, 1)
	}
}

func (s *tokenSuite) TestToken_Validate_MissingTokenFromRequest() {
	if testing.Short() {
		s.T().Skip("skipping test in short mode.")
	}

	e := echo.New()
	e.Validator = dto.NewCustomValidator()

	req := httptest.NewRequest(http.MethodPost, "/token", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cfg := s.setupConfig()
	auditLogger := auditlog.NewLogger(s.storage, cfg.AuditLog)
	handler := NewTokenHandler(cfg, s.storage, sessionManager{}, auditLogger)
	err := handler.Validate(c)
	if s.Error(err) {
		herr, ok := err.(*dto.HTTPError)
		s.True(ok)
		s.Equal(http.StatusBadRequest, herr.Code)
		s.Contains("value is a required field", herr.Message)

		logs, lerr := s.storage.GetAuditLogPersister().List(0, 0, nil, nil, []string{"token_exchange_failed"}, "", "", "", "")
		s.NoError(lerr)
		s.Len(logs, 1)
	}
}

func (s *tokenSuite) TestToken_Validate_InvalidJson() {
	if testing.Short() {
		s.T().Skip("skipping test in short mode.")
	}

	e := echo.New()
	e.Validator = dto.NewCustomValidator()
	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewReader([]byte("invalid")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cfg := s.setupConfig()
	auditLogger := auditlog.NewLogger(s.storage, cfg.AuditLog)
	handler := NewTokenHandler(cfg, s.storage, sessionManager{}, auditLogger)
	err := handler.Validate(c)
	if s.Error(err) {
		herr, ok := err.(*dto.HTTPError)
		s.True(ok)
		s.Equal(http.StatusBadRequest, herr.Code)

		logs, lerr := s.storage.GetAuditLogPersister().List(0, 0, nil, nil, []string{"token_exchange_failed"}, "", "", "", "")
		s.NoError(lerr)
		s.Len(logs, 1)
	}
}

func (s *tokenSuite) TestToken_Validate_TokenNotFound() {
	if testing.Short() {
		s.T().Skip("skipping test in short mode.")
	}

	e := echo.New()
	e.Validator = dto.NewCustomValidator()

	uId := uuid.FromStringOrNil("b5dd5267-b462-48be-b70d-bcd6f1bbe7a5")
	token, err := models.NewToken(uId)
	s.NoError(err)

	body := TokenValidationBody{Value: token.Value}
	bodyJson, err := json.Marshal(body)
	s.NoError(err)

	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewReader(bodyJson))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cfg := s.setupConfig()
	auditLogger := auditlog.NewLogger(s.storage, cfg.AuditLog)
	handler := NewTokenHandler(cfg, s.storage, sessionManager{}, auditLogger)
	err = handler.Validate(c)
	if s.Error(err) {
		herr, ok := err.(*dto.HTTPError)
		s.True(ok)
		s.Equal(http.StatusNotFound, herr.Code)
		s.Equal("token not found", herr.Message)

		logs, lerr := s.storage.GetAuditLogPersister().List(0, 0, nil, nil, []string{"token_exchange_failed"}, "", "", "", "")
		s.NoError(lerr)
		s.Len(logs, 1)
	}
}

func (s *tokenSuite) setupConfig() *config.Config {
	cfg := &defaultConfig
	cfg.Session.EnableAuthTokenHeader = true
	cfg.AuditLog.Storage.Enabled = true
	return cfg
}