# Makefile d'Avanti — harnais de qualité et de build.
#
# Tous les outils sont installés dans ./bin (gitignoré) à des versions épinglées,
# de sorte qu'un contributeur et la CI exécutent exactement les mêmes binaires.
# Point de départ pour un nouveau clone : `make tools && make hooks`.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Racine du dépôt, indépendante du répertoire d'appel (autorise `make -C`).
ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
BIN := $(ROOT)/bin

# Versions épinglées. Chacune a été relevée sur la source officielle (registre Go
# ou GitHub releases) — ne pas les modifier de mémoire, revérifier en amont.
GOLANGCI_VERSION    := v2.12.2
GOSEC_VERSION       := v2.28.0
GOVULNCHECK_VERSION := v1.6.0
GITLEAKS_VERSION    := v8.30.1
GREMLINS_VERSION    := v0.6.0

GOLANGCI    := $(BIN)/golangci-lint
GOSEC       := $(BIN)/gosec
GOVULNCHECK := $(BIN)/govulncheck
GITLEAKS    := $(BIN)/gitleaks
GREMLINS    := $(BIN)/gremlins

# Estampille de build injectée dans internal/platform.
VERSION   ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git -C $(ROOT) rev-parse --short HEAD 2>/dev/null || echo none)
BUILDDATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/Romain-Badino/Avanti/internal/platform.version=$(VERSION) \
	-X github.com/Romain-Badino/Avanti/internal/platform.commit=$(COMMIT) \
	-X github.com/Romain-Badino/Avanti/internal/platform.date=$(BUILDDATE)

.PHONY: help
help: ## Affiche cette aide
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Build & tests ----------------------------------------------------------

.PHONY: build
build: ## Compile le binaire dans ./bin/avanti
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/avanti $(ROOT)/cmd/avanti

.PHONY: test
test: ## Lance les tests avec détecteur de course et couverture
	go test -race -cover -coverprofile=$(ROOT)/coverage.out ./...

.PHONY: test-short
test-short: ## Tests rapides, sans -race (utilisé par le hook pre-commit)
	go test -short ./...

.PHONY: cover
cover: test ## Ouvre le rapport de couverture HTML
	go tool cover -html=$(ROOT)/coverage.out -o $(ROOT)/coverage.html
	@echo "Rapport : $(ROOT)/coverage.html"

.PHONY: tidy
tidy: ## Remet go.mod et go.sum d'aplomb
	go mod tidy

# --- Développement local ----------------------------------------------------
#
# La base de développement vit dans compose.yaml : PostgreSQL seul, sur le port
# hôte 5439 et la boucle locale. Elle n'est ni un modèle de déploiement, ni ce
# qu'utilise la CI.

COMPOSE := docker compose -f $(ROOT)/compose.yaml

.PHONY: dev-db-up
dev-db-up: ## Démarre le PostgreSQL de développement et attend qu'il réponde
	$(COMPOSE) up -d --wait
	@echo "PostgreSQL de développement prêt sur 127.0.0.1:5439."

.PHONY: dev-db-down
dev-db-down: ## Arrête le PostgreSQL de développement (les données sont conservées)
	$(COMPOSE) down

.PHONY: dev-db-reset
dev-db-reset: ## Arrête le PostgreSQL de développement et jette ses données
	$(COMPOSE) down --volumes
	@echo "Volume avanti-dev-pgdata supprimé."

.PHONY: dev-db-psql
dev-db-psql: ## Ouvre un psql sur la base de développement
	$(COMPOSE) exec postgres psql -U avanti -d avanti

# `make run` charge .env s'il existe, pour que la configuration locale n'ait pas
# à être exportée à la main dans chaque shell. Les variables déjà présentes dans
# l'environnement l'emportent : `AVANTI_LOG_LEVEL=debug make run` fonctionne.
#
# set -a exporte automatiquement ce que le fichier définit ; le sous-shell évite
# que ces variables ne fuient dans les autres cibles.
.PHONY: run
run: ## Lance l'application (charge .env s'il existe)
	@set -a; \
	if [ -f $(ROOT)/.env ]; then \
		echo "Chargement de .env"; \
		. $(ROOT)/.env; \
	else \
		echo "Aucun .env : copiez .env.example si le démarrage échoue." >&2; \
	fi; \
	set +a; \
	go run -ldflags '$(LDFLAGS)' $(ROOT)/cmd/avanti serve

# --- Qualité ----------------------------------------------------------------

.PHONY: fmt
fmt: $(GOLANGCI) ## Reformate le code (gofumpt + goimports)
	$(GOLANGCI) fmt

.PHONY: lint
lint: $(GOLANGCI) ## Analyse statique, frontières hexagonales incluses (depguard)
	$(GOLANGCI) run

.PHONY: sec
sec: $(GOSEC) $(GOVULNCHECK) ## Analyse de sécurité du code (gosec) et des dépendances (govulncheck)
	$(GOSEC) -quiet -exclude-generated ./...
	$(GOVULNCHECK) ./...

.PHONY: secrets
secrets: $(GITLEAKS) ## Cherche des secrets dans l'arbre de travail et l'historique
	$(GITLEAKS) dir $(ROOT) --no-banner --redact
	$(GITLEAKS) git $(ROOT) --no-banner --redact

# Tests de mutation : best-effort, hors de `ci` (trop lent pour chaque push).
# Restreint aux domaines, là où vit la logique métier dont la valeur des tests
# mérite d'être mesurée ; les adapters en sont absents, leurs tests sont des tests
# d'intégration que la mutation évalue mal.
#
# --timeout-coefficient 10 : gremlins déduit le délai d'un mutant de la durée de
# la suite de référence. Sur une suite rapide, le délai calculé est si court que
# tous les mutants sortent en TIMED OUT au lieu d'être jugés. Le coefficient
# corrige ce biais — vérifié : sans lui 3 mutants sur 3 expirent, avec lui ils
# sont correctement classés KILLED/LIVED.
#
# Un paquet par appel, et non un motif récursif : `gremlins unleash ./internal/...`
# rend « No results to report » sans rien analyser — v0.6.0 n'étend pas le `...`
# de Go. La panne était silencieuse, ce qui est le pire cas pour une cible dont on
# lit la sortie plutôt que le code de retour. Énumérer les domaines coûte une
# ligne à chaque nouveau domaine, et se voit.
#
# Best-effort assumé : gremlins avance lentement (v0.6.0 en décembre 2025) et sa
# sortie est indicative, pas un quality gate. Voir docs/ARCHITECTURE.md.
DOMAINES := devis document finance identity planning

.PHONY: mutation
mutation: $(GREMLINS) ## Tests de mutation sur les domaines (best-effort, lent)
	@for domaine in $(DOMAINES); do \
		echo "--- internal/$$domaine"; \
		$(GREMLINS) unleash --timeout-coefficient 10 ./internal/$$domaine \
			|| echo "make mutation : gremlins a terminé en erreur sur $$domaine — cible best-effort, sans incidence sur la CI" >&2; \
	done

.PHONY: ci
ci: lint test sec secrets ## Enchaîne tout ce que la CI vérifie
	@echo "make ci : tout est vert."

# --- Outillage --------------------------------------------------------------

.PHONY: tools
tools: $(GOLANGCI) $(GOSEC) $(GOVULNCHECK) $(GITLEAKS) $(GREMLINS) ## Installe l'outillage épinglé dans ./bin
	@echo "Outils installés dans $(BIN)."

$(GOLANGCI):
	@mkdir -p $(BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
		| sh -s -- -b $(BIN) $(GOLANGCI_VERSION)

$(GOSEC):
	GOBIN=$(BIN) go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)

$(GOVULNCHECK):
	GOBIN=$(BIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Le dépôt est gitleaks/gitleaks mais le module n'a jamais été renommé : son
# chemin canonique reste github.com/zricethezav/gitleaks. `go install` refuse
# l'alias, d'où ce chemin d'apparence périmé — il est correct.
$(GITLEAKS):
	GOBIN=$(BIN) go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

$(GREMLINS):
	GOBIN=$(BIN) go install github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)

.PHONY: hooks
hooks: ## Active les hooks git du dépôt (.githooks)
	git -C $(ROOT) config core.hooksPath .githooks
	@chmod +x $(ROOT)/.githooks/*
	@echo "core.hooksPath pointe sur .githooks."

.PHONY: clean
clean: ## Supprime les artefacts de build et de couverture
	rm -rf $(BIN)
	rm -f $(ROOT)/coverage.out $(ROOT)/coverage.html
