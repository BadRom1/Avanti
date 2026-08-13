// Package server monte le serveur HTTP d'Avanti : délais d'attente, sondes
// d'exploitation, intergiciels de base et arrêt gracieux.
//
// Il ne sait rien des pages qu'il sert. Les routes applicatives lui arrivent
// sous la forme d'un http.Handler que cmd/avanti lui passe — c'est ce qui
// permet au socle d'ignorer l'existence des adapters (R3 de
// docs/ARCHITECTURE.md).
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
)

// Délais d'attente du serveur. Aucun n'est laissé à zéro : un serveur sans
// délai garde indéfiniment une connexion ouverte, ce qui suffit à l'épuiser
// depuis l'extérieur.
const (
	// readHeaderTimeout borne l'envoi des en-têtes. C'est la protection contre
	// les clients qui ouvrent une connexion et l'alimentent au goutte-à-goutte.
	readHeaderTimeout = 5 * time.Second
	// readTimeout borne la lecture complète de la requête. Il doit rester
	// confortable : le téléversement d'un scan de facture passe par là.
	readTimeout = 2 * time.Minute
	// writeTimeout borne l'écriture de la réponse, exports PDF compris.
	writeTimeout = 2 * time.Minute
	// idleTimeout borne l'attente entre deux requêtes d'une même connexion.
	idleTimeout = 2 * time.Minute
	// readinessTimeout borne la sonde de /readyz : un orchestrateur attend une
	// réponse rapide, pas un diagnostic exhaustif.
	readinessTimeout = 3 * time.Second
)

// ReadinessCheck décide si l'instance est en état de recevoir du trafic. Elle
// renvoie nil quand tout va bien, et une erreur explicite sinon.
type ReadinessCheck func(ctx context.Context) error

// Options rassemble ce qu'il faut pour construire un serveur.
type Options struct {
	// Config fournit l'adresse d'écoute et le délai d'arrêt gracieux.
	Config *config.Config
	// Logger reçoit les journaux d'accès et de cycle de vie.
	Logger *slog.Logger
	// Handler porte les routes applicatives, montées à la racine. Les sondes
	// d'exploitation sont ajoutées par-dessus et ont la priorité.
	Handler http.Handler
	// Ready est la sonde de /readyz. Nil signifie « rien à vérifier ».
	Ready ReadinessCheck
}

// Server enveloppe un http.Server et son cycle de vie.
type Server struct {
	http            *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// New assemble le serveur : routes d'exploitation, routes applicatives, puis la
// chaîne d'intergiciels — récupération de panique, identifiant de requête,
// journal d'accès, dans cet ordre de l'extérieur vers l'intérieur.
//
// L'ordre n'est pas indifférent. La récupération est la plus externe pour
// couvrir aussi les paniques des intergiciels ; l'identifiant est posé avant le
// journal pour que chaque ligne d'accès le porte.
func New(opts Options) (*Server, error) {
	switch {
	case opts.Config == nil:
		return nil, errors.New("server : configuration manquante")
	case opts.Logger == nil:
		return nil, errors.New("server : journal manquant")
	}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleLive(opts.Logger))
	mux.Handle("GET /readyz", handleReady(opts.Logger, opts.Ready))

	if opts.Handler != nil {
		mux.Handle("/", opts.Handler)
	}

	var root http.Handler = mux
	root = accessLog(opts.Logger)(root)
	root = requestID(root)
	root = recoverPanic(opts.Logger)(root)

	return &Server{
		http: &http.Server{
			Addr:              opts.Config.ListenAddr,
			Handler:           root,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
		},
		logger:          opts.Logger,
		shutdownTimeout: opts.Config.ShutdownTimeout,
	}, nil
}

// Handler expose la chaîne complète telle qu'elle est servie, pour les tests qui
// veulent l'exercer sans ouvrir de port.
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// Run écoute sur l'adresse configurée et sert jusqu'à l'annulation de ctx.
func (s *Server) Run(ctx context.Context) error {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("écoute sur %s : %w", s.http.Addr, err)
	}

	return s.Serve(ctx, listener)
}

// Serve sert sur listener jusqu'à l'annulation de ctx, puis laisse aux requêtes
// en cours le temps configuré pour se terminer.
//
// L'annulation vient de cmd/avanti, qui la déclenche sur SIGINT et SIGTERM : le
// socle n'installe pas de gestionnaire de signal lui-même, cela reviendrait à
// décider du cycle de vie du processus à la place de son point d'entrée.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	served := make(chan error, 1)

	go func() {
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		served <- err
	}()

	s.logger.Info("serveur HTTP à l'écoute", slog.String("addr", listener.Addr().String()))

	select {
	case err := <-served:
		if err != nil {
			return fmt.Errorf("service HTTP : %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	s.logger.Info("arrêt demandé, attente des requêtes en cours",
		slog.Duration("timeout", s.shutdownTimeout))

	// context.WithoutCancel : ctx vient d'être annulé, s'en servir tel quel
	// couperait l'arrêt gracieux à l'instant même où il commence.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("arrêt du serveur HTTP : %w", err)
	}
	if err := <-served; err != nil {
		return fmt.Errorf("service HTTP : %w", err)
	}

	s.logger.Info("serveur HTTP arrêté")

	return nil
}
