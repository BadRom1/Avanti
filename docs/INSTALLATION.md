# Installer Avanti chez soi

Ce document mène d'une machine nue à une instance d'Avanti en production :
construction, configuration, premiers comptes, reverse proxy, branchement d'un
agent Claude, sauvegardes. Il est prescriptif comme le reste de la
documentation : quand un choix est imposé, la raison est donnée avec.

Le modèle de déploiement est celui de `docs/ARCHITECTURE.md` §1 : **un binaire,
une base PostgreSQL**, rien d'autre. Deux chemins y mènent — Docker Compose,
le plus simple, ou le binaire nu face à un PostgreSQL administré à part. Les
étapes 3 à 7 sont communes aux deux.

## 1. Prérequis

**Chemin Docker** : Docker Engine avec le plugin Compose, et `git` pour cloner
le dépôt. C'est tout — le binaire se construit dans l'image.

**Chemin binaire** : Go (la version de la directive `go` de `go.mod`), `make`,
`git`, et un PostgreSQL administré par vos soins — **version 18**, c'est celle
que le développement et la CI exercent ; une version antérieure récente est
plausible mais non exercée. Le binaire embarque ses migrations, ses gabarits,
ses feuilles de style et son catalogue de traductions : une fois compilé, il se
déplace seul.

Dans les deux cas : un nom de domaine pointant sur la machine, et de quoi
terminer TLS devant l'application (étape 5). Une instance qui détient des
documents d'assurance et des données financières ne se sert pas en clair.

## 2. Construire

**Docker** — depuis la racine du dépôt :

```sh
docker build \
  --build-arg VERSION="$(git describe --tags --always --dirty)" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  --build-arg BUILDDATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t avanti .
```

L'image finale est distroless : ni shell, ni gestionnaire de paquets, un seul
exécutable et les certificats CA. Les trois `--build-arg` estampillent le
binaire (`avanti version`) ; les omettre donne une image fonctionnelle marquée
`dev`.

**Binaire** — `make build` produit `./bin/avanti`, statique et autonome.
`file ./bin/avanti` doit dire `statically linked` : c'est ce qui permet de le
copier sur le serveur sans rien installer d'autre.

## 3. Configurer

Toute la configuration passe par des variables d'environnement `AVANTI_*`,
décrites une à une dans [.env.example](../.env.example). Copiez-le et ajustez :

```sh
cp .env.example .env
```

Quatre décisions comptent ; le reste a des valeurs par défaut raisonnables. En
particulier, laissez `AVANTI_LISTEN_ADDR` commentée : le défaut `:8080`
convient partout — dans un conteneur, c'est la ligne `ports` du compose qui
restreint l'exposition ; ne la renseignez (`127.0.0.1:8080`) que pour le
binaire nu derrière un reverse proxy local.

**`AVANTI_ENV=production`.** Ce réglage fait plus que passer les journaux en
JSON : en production, la configuration **refuse les valeurs d'exemple**
(préfixe `change-me`) publiées dans ce dépôt — la clé OAuth, le mot de passe
PostgreSQL de la chaîne de connexion, les identifiants S3. Un secret oublié
arrête le démarrage en nommant la variable fautive, au lieu de laisser tourner
une instance dont la clé est publique.

**`AVANTI_OAUTH_SECRET`.** La clé HMAC qui signe les codes d'autorisation et
les jetons des agents IA. Engendrez-la une fois pour toutes :

```sh
openssl rand -base64 32
```

Trente-deux octets au minimum. La changer déconnecte d'un coup tous les agents
autorisés — c'est un inconvénient au quotidien et un outil le jour où il faut
tout révoquer (étape 7).

**`AVANTI_BASE_URL`.** L'URL publique réelle de l'instance,
`https://avanti.exemple.fr`. Elle sert aux liens absolus, à l'origine de
confiance CSRF et à l'émetteur OAuth : une valeur fausse ne casse pas tout de
suite, elle casse au premier agent qui compare l'émetteur annoncé à l'adresse
qu'il a appelée. Elle doit être la **racine d'un hôte, sans chemin** — les
documents de découverte OAuth et MCP sont cherchés sous `/.well-known` à la
racine, une instance sous préfixe serait introuvable ; le démarrage refuse un
chemin, utilisez un sous-domaine.

**Le stockage des documents.** Par défaut (`AVANTI_STORAGE_BACKEND=filesystem`),
le contenu binaire des pièces va dans `AVANTI_DOCUMENTS_DIR`. Ce répertoire est
confidentiel et fait partie du périmètre de sauvegarde au même titre que la
base. Exigence technique à connaître : l'écriture atomique repose sur les
**liens durs**, il faut donc un système de fichiers qui les supporte (ext4,
xfs, btrfs…) — pas de FAT/exFAT ni certains montages réseau. L'alternative est
un service compatible S3 (`AVANTI_STORAGE_BACKEND=s3`, variables `AVANTI_S3_*`),
auquel cas c'est le seau qui se sauvegarde.

**Avec Docker Compose** : le dépôt fournit
[compose.production.yaml](../compose.production.yaml), un exemple commenté qui
lève l'application et son PostgreSQL. Le même `.env` sert aux deux : le service
`avanti` le charge en entier (`env_file`), le service `postgres` n'en reçoit
que `POSTGRES_PASSWORD` (interpolé — la clé OAuth n'a rien à faire dans son
environnement). Un volume nommé porte les documents, et l'application n'est
exposée que sur `127.0.0.1:8080` — le reverse proxy de l'étape 5 fait le
reste. L'en-tête du fichier liste les lignes du `.env` à ajuster.

## 4. Premier démarrage et premiers comptes

Les migrations sont embarquées et rejouées à chaque démarrage
(`AVANTI_MIGRATE_ON_START=true`, le défaut) : il n'y a **aucun schéma à créer à
la main**, une base vide suffit.

Avanti n'a **pas de page d'inscription, et n'en aura pas** : une instance
privée n'a personne à inscrire, et un formulaire ouvert sur Internet serait une
porte à surveiller pour un besoin qui n'existe pas. Les comptes se créent en
ligne de commande, sur la machine qui héberge l'instance :

```sh
# Docker Compose :
docker compose -f compose.production.yaml exec avanti /avanti user add \
  --email vous@exemple.fr --nom "Votre Nom" --role proprietaire --generate

# Binaire :
avanti user add --email vous@exemple.fr --nom "Votre Nom" --role proprietaire
```

Sans `--generate`, le mot de passe est demandé au terminal, sans écho ; avec,
il est engendré et affiché une seule fois. Les sous-commandes `user` lisent la
même configuration que le serveur et appliquent les migrations manquantes : le
premier compte se crée donc avant que le serveur n'ait jamais tourné.

Deux rôles existent : `proprietaire` (tout, accès agent IA compris) et
`collaborateur` (devis et planning seulement, sans accès agent) — la table
exacte est dans `docs/ARCHITECTURE.md` §4. `avanti user list` montre qui
existe, `avanti user disable` ferme un accès sans rien supprimer.

Cette CLI, exécutée sur l'hôte, est la **racine de confiance** de l'instance :
tout ce qui touche aux comptes passe par elle. Un **mot de passe perdu** se
répare par `avanti user set-password --email … [--generate]` (Avanti ne peut
pas le retrouver, seule son empreinte argon2id est stockée) ; un **changement
de rôle** par `avanti user set-role --email … --role proprietaire|collaborateur`,
à effet immédiat — le rôle est relu à chaque requête, sessions ouvertes et
jetons d'agent compris.

Vérifiez ensuite que l'instance répond :

```sh
curl -i http://127.0.0.1:8080/healthz   # le processus répond
curl -i http://127.0.0.1:8080/readyz    # la base répond aussi
```

Ces deux sondes sont le point d'entrée de la supervision. L'image Docker étant
distroless (pas de shell), un `HEALTHCHECK` interne est impossible : c'est
l'orchestrateur ou le reverse proxy qui interroge `/healthz`.

Pour essayer l'application avec des données réalistes avant de saisir les
vraies, `avanti seed demo --email vous@exemple.fr` remplit une instance
**vide** d'un jeu de démonstration complet. La commande refuse de tourner en
production et dès que la base contient la moindre donnée métier : c'est un
outil de découverte, pas d'exploitation.

## 5. Reverse proxy : TLS obligatoire

En production, Avanti vit derrière un reverse proxy qui termine TLS. Trois
exigences, toutes trois raisonnées :

- **TLS n'est pas négociable.** Sessions, mots de passe, jetons OAuth,
  documents d'assurance : tout transite par ce canal. `AVANTI_BASE_URL` doit
  être l'URL en `https`.
- **Transmettre l'en-tête `Host` d'origine.** La protection CSRF déclare l'URL
  publique comme origine de confiance : un proxy qui réécrit `Host` ferait
  refuser des requêtes légitimes. Avec nginx : `proxy_set_header Host $host;` ;
  Caddy le fait par défaut.
- **Savoir ce que devient la limite anti-force-brute.** Le formulaire de
  connexion bloque temporairement au-delà d'un seuil d'échecs, comptés par
  couple (compte visé, adresse IP de la connexion TCP). `X-Forwarded-For` est
  **délibérément ignoré** : c'est un en-tête que n'importe quel client peut
  écrire, s'y fier offrirait un compteur neuf à chaque requête forgée.
  Conséquence honnête : derrière un proxy, tout le trafic porte l'adresse du
  proxy, et la limite dégénère en limite par compte — toujours utile contre le
  bourrinage d'un mot de passe, mais elle ne distingue plus les attaquants des
  utilisateurs légitimes. C'est un compromis connu et documenté
  (`docs/ARCHITECTURE.md` §4), pas un oubli ; une liste de proxys de confiance
  en configuration est la suite envisagée si ce déploiement devient la norme.

Exemple Caddy complet — TLS automatique, `Host` transmis :

```
avanti.exemple.fr {
    reverse_proxy 127.0.0.1:8080
}
```

## 6. Brancher Claude (accès agent par MCP)

L'instance expose un serveur MCP sur **`https://avanti.exemple.fr/mcp`**. Tout
le reste se découvre tout seul :

1. le client MCP interroge le serveur, reçoit un 401 qui pointe vers le
   document *Protected Resource Metadata* (RFC 9728), lequel désigne le serveur
   d'autorisation OAuth 2.1 embarqué ;
2. l'agent **s'enregistre seul** comme client OAuth (enregistrement dynamique,
   RFC 7591) — aucune déclaration manuelle, aucun secret à copier ;
3. un **propriétaire consent dans son navigateur** : la page de consentement,
   servie par l'instance derrière la session web, affiche ce qui sera réellement
   accordé — l'intersection de ce que l'agent demande et de ce que le compte
   détient — et l'autorisation peut se retirer sans toucher au compte.

Concrètement, il n'y a donc **que l'URL à donner**.

**Claude Code** :

```sh
claude mcp add --transport http avanti https://avanti.exemple.fr/mcp
```

À la première utilisation, `/mcp` dans Claude Code ouvre le navigateur pour le
consentement.

**claude.ai** (web et applications) : Paramètres → Connecteurs → « Ajouter un
connecteur personnalisé », avec la même URL `https://avanti.exemple.fr/mcp`.
Le consentement s'ouvre dans le navigateur, sur votre instance.

Ce que l'agent obtient est borné deux fois : par les scopes consentis, et par
le rôle du compte à **chaque** requête — une désactivation ou une rétrogradation
prend effet immédiatement, jetons déjà émis compris. Un compte `collaborateur`
ne peut pas ouvrir d'accès agent : son rôle ne porte pas le scope `mcp`, et le
refus est celui du compte, pas de la demande. Les outils exposés consultent les
quatre domaines et écrivent devis, factures, acomptes et étapes ; **aucun outil
n'envoie quoi que ce soit** — toute transmission à l'assurance reste un geste
humain, l'agent ne fait que la préparer.

## 7. Sauvegardes et révocation

Deux choses à sauvegarder, ensemble :

```sh
# La base — tout sauf le contenu binaire des pièces :
docker compose -f compose.production.yaml exec postgres \
  pg_dump -U avanti -Fc avanti > avanti-$(date +%F).dump

# Les documents (backend filesystem) — le volume ou le répertoire :
docker run --rm -v avanti_avanti-documents:/data -v "$PWD:/backup" \
  busybox tar czf /backup/documents-$(date +%F).tar.gz -C /data .
```

Avec le binaire nu : `pg_dump` sur la base, et une archive de
`AVANTI_DOCUMENTS_DIR`. Les deux vont ensemble — une base sans ses documents
liste des pièces introuvables, des documents sans la base sont des fichiers
sans nom. Testez la restauration une fois avant d'en avoir besoin.

**Rotation du secret OAuth.** Changer `AVANTI_OAUTH_SECRET` invalide d'un coup
tous les jetons émis : chaque agent devra être ré-autorisé par un
consentement neuf. C'est la procédure de révocation globale — clé compromise,
machine d'un agent perdue, doute raisonnable — et elle ne touche ni aux
comptes, ni aux sessions web, ni aux données.
