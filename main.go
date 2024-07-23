package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bobg/mid"
	oauth2errs "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/manage"
	"github.com/go-oauth2/oauth2/v4/models"
	"github.com/go-oauth2/oauth2/v4/server"
	"github.com/go-oauth2/oauth2/v4/store"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	ServeAt string `toml:"serve-at"`
	Users   []User `toml:"user"`
}

type User struct {
	ID       string `toml:"id"`
	Secret   string `toml:"unsafe-secret"`
	Domain   string `toml:"domain"`
	Password string `toml:"unsafe-password"`
}

var exampleConfig = &Config{
	ServeAt: "localhost:9478",
	Users: []User{
		{
			ID:       "1234",
			Secret:   "test-secret",
			Password: "test-password",
			Domain:   "localhost:8080",
		},
	},
}

func loadConfig(filename string) (*Config, error) {
	data, errLoad := os.ReadFile(filename)
	if errLoad != nil {
		return nil, fmt.Errorf("parsing ini: %w", errLoad)
	}

	cfg := new(Config)
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing toml: %w", errLoad)
	}

	return cfg, nil
}

func main() {
	configFilename := "isolenta.toml"
	flag.StringVar(&configFilename, "config", configFilename, "configuration config file")

	printExampleConfig := false
	flag.BoolVar(&printExampleConfig, "example-config", printExampleConfig, "print example TOML config to stdout and exit")

	flag.Parse()

	if printExampleConfig {
		data, err := toml.Marshal(exampleConfig)
		if err != nil {
			panic(err)
		}
		fmt.Printf("# > isolenta.toml\n\n%s\n", data)
		return
	}

	cfg, errCfg := loadConfig(configFilename)
	if errCfg != nil {
		panic("loading config file " + configFilename + ": " + errCfg.Error())
	}

	manager := manage.NewDefaultManager()
	manager.MustTokenStorage(store.NewMemoryTokenStore())

	clientStore := store.NewClientStore()

	userpasswd := map[string]string{}
	for _, client := range cfg.Users {
		clientStore.Set(client.ID, &models.Client{
			ID:     client.ID,
			UserID: client.ID,
			Secret: client.Secret,
			Domain: client.Domain,
		})
		if client.Password != "" {
			userpasswd[client.ID] = client.Password
		}
	}

	manager.MapClientStorage(clientStore)

	srv := server.NewDefaultServer(manager)
	srv.SetAllowGetAccessRequest(true)
	srv.SetClientInfoHandler(server.ClientFormHandler)

	srv.SetInternalErrorHandler(func(err error) (re *oauth2errs.Response) {
		log.Println("ERROR: internal ", err)
		return
	})

	srv.SetResponseErrorHandler(func(re *oauth2errs.Response) {
		log.Printf("WARN: user-side %+v", re)
	})

	srv.SetPasswordAuthorizationHandler(func(ctx context.Context, clientID, username, password string) (userID string, err error) {
		wantPasswd := userpasswd[clientID]
		if wantPasswd != password {
			return "", fmt.Errorf("%q: %w", username, oauth2errs.ErrAccessDenied)
		}
		return clientID, nil
	})

	srv.UserAuthorizationHandler = func(w http.ResponseWriter, r *http.Request) (userID string, err error) {
		query := r.URL.Query()
		id := query.Get("client_id")
		if id == "" {
			return "", oauth2errs.ErrAccessDenied
		}

		return id, nil
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		err := srv.HandleAuthorizeRequest(w, r)
		if err != nil {
			log.Printf("ERROR: /authorize: %s", err)
			serveError(w, err)
		}
	})

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		err := srv.HandleTokenRequest(w, r)
		if err != nil {
			log.Printf("ERROR: /token: %s", err)
			serveError(w, err)
		}
	})

	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		tok, errTok := srv.Manager.LoadAccessToken(r.Context(), query.Get("access_token"))
		if errTok != nil {
			log.Printf("ERROR: /userinfo: %v", errTok)
			serveError(w, errTok)
			return
		}

		data, _ := json.MarshalIndent(srv.GetTokenData(tok), "", "  ")

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	errServe := (&http.Server{
		Addr:              cfg.ServeAt,
		ReadHeaderTimeout: time.Second,
		Handler:           mid.Log(mux),
	}).ListenAndServe()
	if errServe != nil {
		panic("serving at " + cfg.ServeAt + ": " + errServe.Error())
	}
}

func serveError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}

	code, ok := oauth2errs.StatusCodes[err]
	if ok {
		msg := http.StatusText(code) + "\n" + err.Error() + "\n"
		http.Error(w, msg, code)
		return
	}

	http.Error(w, "internal error", http.StatusInternalServerError)
}
