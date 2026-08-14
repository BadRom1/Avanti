package document

import "errors"

// Les erreurs du domaine. Elles sont comparables avec errors.Is, y compris au
// travers des enveloppes que les adapters ajoutent.
var (
	// ErrEmptyFileName signale une pièce sans nom de fichier exploitable. Le nom
	// est ce qui identifie la pièce pour un humain — « devis-charpente.pdf » —
	// et c'est aussi ce que le téléchargement rendra ; sans lui, la liste des
	// pièces devient illisible.
	ErrEmptyFileName = errors.New("document : le nom de fichier est obligatoire")
	// ErrFileNameTooLong signale un nom de fichier au-delà de la borne, une
	// fois nettoyé.
	ErrFileNameTooLong = errors.New("document : nom de fichier trop long")
	// ErrDescriptionTooLong signale une description au-delà de sa borne.
	ErrDescriptionTooLong = errors.New("document : description trop longue")
	// ErrFileTooLarge signale un contenu au-delà de [MaxFileSize]. La borne
	// protège le stockage et la mémoire du serveur ; une pièce plus grosse est
	// presque toujours un mauvais scan à refaire, pas un besoin.
	ErrFileTooLarge = errors.New("document : fichier trop volumineux")
	// ErrEmptyContent signale un contenu absent ou de taille nulle. Une pièce
	// vide ne justifie rien : c'est un téléversement raté, à refuser tôt.
	ErrEmptyContent = errors.New("document : contenu vide")
	// ErrUnsupportedMimeType signale un type de fichier hors de la liste
	// d'autorisation. La liste est fermée à dessein : chaque format accepté est
	// un format que le navigateur d'un propriétaire ouvrira un jour, et le SVG
	// ou le HTML y seraient des vecteurs d'exécution de script.
	ErrUnsupportedMimeType = errors.New("document : type de fichier non pris en charge")
	// ErrUnknownCategory signale une catégorie hors de celles que le domaine
	// reconnaît.
	ErrUnknownCategory = errors.New("document : catégorie inconnue")
	// ErrInvalidTarget signale un rattachement incohérent : un type sans
	// identifiant, un identifiant sans type, ou un type de cible inconnu.
	ErrInvalidTarget = errors.New("document : rattachement invalide")
	// ErrMissingActor signale un dépôt sans acteur. La traçabilité n'est pas
	// facultative : une pièce anonyme ne s'explique plus six mois après.
	ErrMissingActor = errors.New("document : acteur du dépôt manquant")

	// ErrUnknownDocument signale l'absence de la pièce cherchée. C'est l'erreur
	// que rend un [Repository] sur une lecture sans résultat.
	ErrUnknownDocument = errors.New("document : pièce inconnue")

	// ErrContentNotFound signale un contenu absent du stockage. C'est l'erreur
	// qu'un [Storage] rend — éventuellement enveloppée — quand la clé demandée
	// ne désigne rien.
	ErrContentNotFound = errors.New("document : contenu introuvable dans le stockage")
	// ErrContentAlreadyExists signale une clé de stockage déjà occupée. Un
	// [Storage] la rend quand [Storage.Save] retrouve la clé : les clés sont
	// des identifiants tirés au hasard, une collision est un bug ou une
	// réécriture — dans les deux cas, écraser silencieusement serait pire.
	ErrContentAlreadyExists = errors.New("document : contenu déjà présent dans le stockage")
	// ErrContentSizeMismatch signale un contenu dont la longueur réelle ne
	// correspond pas à la taille annoncée par l'appelant. Ce n'est pas une
	// faute de saisie qu'un utilisateur peut corriger : l'adapter qui appelle
	// [Service.Upload] est censé annoncer la taille qu'il a constatée, et un
	// écart est un bug de cet adapter — l'erreur doit finir en panne, pas en
	// message de formulaire.
	ErrContentSizeMismatch = errors.New("document : taille annoncée différente du contenu transmis")
)
