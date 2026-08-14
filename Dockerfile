# Dockerfile d'Avanti — image de production en deux étages.
#
# Étage 1 : compilation d'un binaire statique. La version de Go est celle de la
# directive `go` de go.mod (1.26.6) : l'image et le module doivent avancer
# ensemble, sans quoi le build échoue au premier écart. CGO_ENABLED=0 rend le
# binaire autonome — aucune libc requise — ce qui est la condition de l'étage
# final. Migrations SQL, gabarits HTML, CSS, HTMX et catalogue i18n sont
# embarqués dans le binaire (go:embed) : rien d'autre n'est à copier.
#
# Étage 2 : gcr.io/distroless/static-debian12:nonroot. Le choix est raisonné :
#
#   - `static` : aucune libc, aucun shell, aucun gestionnaire de paquets — la
#     surface d'attaque se réduit au binaire lui-même. C'est possible parce que
#     l'étage 1 produit un exécutable statique ;
#   - `debian12` : les certificats CA du système y sont présents, ce dont le
#     backend de stockage S3 a besoin pour un point de terminaison en TLS
#     (AVANTI_S3_USE_SSL=true, le défaut). Une image `scratch` les perdrait ;
#   - `nonroot` : le processus tourne sous l'utilisateur non privilégié 65532,
#     sans qu'aucune directive USER puisse être oubliée.
#
# Pas de HEALTHCHECK : distroless n'a ni shell ni curl/wget pour l'exécuter.
# La sonde est l'affaire de l'orchestrateur — GET /healthz (le processus
# répond) ou /readyz (la base aussi) — voir compose.production.yaml et
# docs/INSTALLATION.md.
#
# Construction, depuis la racine du dépôt :
#
#   docker build \
#     --build-arg VERSION="$(git describe --tags --always --dirty)" \
#     --build-arg COMMIT="$(git rev-parse --short HEAD)" \
#     --build-arg BUILDDATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
#     -t avanti .

FROM golang:1.26.6 AS build

WORKDIR /src

# Les dépendances d'abord, seules : tant que go.mod et go.sum ne changent pas,
# cette couche reste en cache et une modification du code ne retélécharge rien.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# L'estampille de build, injectée dans internal/platform comme le fait le
# Makefile. Les valeurs par défaut sont celles d'un build hors git ; les vraies
# se passent en --build-arg (voir l'en-tête).
ARG VERSION=dev
ARG COMMIT=none
ARG BUILDDATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w \
	-X github.com/Romain-Badino/Avanti/internal/platform.version=${VERSION} \
	-X github.com/Romain-Badino/Avanti/internal/platform.commit=${COMMIT} \
	-X github.com/Romain-Badino/Avanti/internal/platform.date=${BUILDDATE}" \
	-o /avanti ./cmd/avanti

# Le répertoire des documents est préparé ici, avec le propriétaire de l'étage
# final : un volume nommé monté sur un chemin absent de l'image appartiendrait
# à root, et l'utilisateur 65532 ne pourrait pas y écrire. Copié depuis l'étage
# de build parce que distroless n'a pas de shell pour un mkdir.
RUN mkdir -p /data/documents

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /avanti /avanti
# 65532 est l'uid/gid de l'utilisateur nonroot de distroless — en numérique,
# pour ne pas dépendre de la résolution du nom dans l'image finale.
COPY --from=build --chown=65532:65532 /data /data

# Le port de la valeur par défaut d'AVANTI_LISTEN_ADDR (:8080). EXPOSE est
# documentaire : c'est la publication du port, dans compose ou l'orchestrateur,
# qui décide de ce qui est joignable.
EXPOSE 8080

# La configuration passe par les variables AVANTI_* (voir .env.example). Seul
# le répertoire des documents a besoin d'une valeur propre à l'image : le
# chemin préparé ci-dessus, inscrit ici pour que l'image fonctionne sans
# qu'on y pense. Tout reste surchargeable à l'exécution.
ENV AVANTI_DOCUMENTS_DIR=/data/documents

ENTRYPOINT ["/avanti"]
CMD ["serve"]
