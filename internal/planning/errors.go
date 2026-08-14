package planning

import "errors"

// Les erreurs du domaine. Elles sont comparables avec errors.Is, y compris au
// travers des enveloppes que les adapters ajoutent.
var (
	// ErrEmptyName signale une étape ou un jalon sans nom. Le nom est ce qui
	// s'affiche sur le Gantt et dans les refus de démarrage : sans lui, rien de
	// ce que le domaine calcule ne se raconte.
	ErrEmptyName = errors.New("planning : le nom est obligatoire")
	// ErrTextTooLong signale un texte au-delà de la borne de son champ.
	ErrTextTooLong = errors.New("planning : texte trop long")
	// ErrMissingDate signale une date obligatoire absente — dates prévues d'une
	// étape, date d'un jalon, date d'une transition.
	ErrMissingDate = errors.New("planning : date obligatoire manquante")
	// ErrInvalidPlannedRange signale une fin prévue antérieure au début prévu.
	ErrInvalidPlannedRange = errors.New("planning : la fin prévue précède le début prévu")
	// ErrMissingActor signale une action sans acteur. La traçabilité n'est pas
	// facultative : une replanification anonyme ne s'explique plus six mois
	// après.
	ErrMissingActor = errors.New("planning : acteur de l'action manquant")
	// ErrInvalidDevisID signale une référence de devis qui ne peut pas être
	// stockée. Le domaine ne sait pas vérifier que le devis existe — c'est une
	// référence faible (R2) — mais il borne ce qu'un POST forgé peut y glisser.
	ErrInvalidDevisID = errors.New("planning : référence de devis invalide")

	// ErrSelfDependency signale une étape qui se déclarerait son propre
	// prérequis : le plus petit des cycles, refusé avant même le parcours du
	// graphe.
	ErrSelfDependency = errors.New("planning : une étape ne peut pas dépendre d'elle-même")
	// ErrDuplicateDependency signale un prérequis déclaré deux fois. Un doublon
	// ne casse rien mais ment sur le graphe : le refuser garde les dépendances
	// lisibles telles qu'elles sont stockées.
	ErrDuplicateDependency = errors.New("planning : prérequis en double")
	// ErrTooManyDependencies signale une liste de prérequis au-delà de la borne
	// du domaine — une soumission forgée, pas un chantier réel.
	ErrTooManyDependencies = errors.New("planning : trop de prérequis")
	// ErrUnknownDependency signale un prérequis qui ne désigne aucune étape.
	ErrUnknownDependency = errors.New("planning : prérequis inconnu")
	// ErrDependencyCycle signale l'erreur métier centrale du graphe : des
	// dépendances qui bouclent. Un cycle rendrait le chantier indémarrable —
	// chaque étape attendrait une étape qui l'attend. Un [Repository] la rend
	// aussi quand la vérification rejouée sous verrou échoue : deux éditions
	// simultanées ne peuvent pas fabriquer un cycle à elles deux.
	ErrDependencyCycle = errors.New("planning : les dépendances forment un cycle")
	// ErrDependenciesLocked signale une tentative de modifier les prérequis
	// d'une étape déjà démarrée. Les dépendances sont la garde du démarrage :
	// une fois l'étape lancée, elles ont joué leur rôle, et les réécrire après
	// coup réécrirait l'histoire — le refus qui a eu lieu (ou pas) l'a été sur
	// ces prérequis-là.
	ErrDependenciesLocked = errors.New("planning : les prérequis d'une étape démarrée ne se modifient plus")

	// ErrEtapeAlreadyStarted signale un double démarrage : l'étape a déjà une
	// date de début réel, et un début ne se reprend pas.
	ErrEtapeAlreadyStarted = errors.New("planning : cette étape est déjà démarrée")
	// ErrEtapeNotStarted signale une terminaison sans démarrage : une étape se
	// termine après avoir commencé, pas à la place.
	ErrEtapeNotStarted = errors.New("planning : cette étape n'est pas démarrée")
	// ErrEtapeAlreadyFinished signale une double terminaison.
	ErrEtapeAlreadyFinished = errors.New("planning : cette étape est déjà terminée")
	// ErrFinishBeforeStart signale une fin réelle antérieure au début réel.
	ErrFinishBeforeStart = errors.New("planning : la fin réelle précède le début réel")
	// ErrPrerequisitesNotDone signale l'invariant central du domaine : une
	// étape ne démarre pas avant que ses prérequis soient terminés. Le message
	// nomme les étapes bloquantes. Un [Repository] la rend aussi quand la
	// vérification rejouée sous verrou échoue — c'est elle qui fait foi, celle
	// du service ne sert qu'à répondre tôt et clairement.
	ErrPrerequisitesNotDone = errors.New("planning : des prérequis ne sont pas terminés")

	// ErrJalonAlreadyReached signale un jalon qu'on tente d'atteindre deux
	// fois. Atteindre un jalon ne se reprend pas.
	ErrJalonAlreadyReached = errors.New("planning : ce jalon est déjà atteint")

	// ErrUnknownEtape signale l'absence de l'étape cherchée. C'est l'erreur que
	// rend un [Repository] sur une lecture ou une réécriture sans résultat.
	ErrUnknownEtape = errors.New("planning : étape inconnue")
	// ErrUnknownJalon signale l'absence du jalon cherché.
	ErrUnknownJalon = errors.New("planning : jalon inconnu")
	// ErrConcurrentUpdate signale une réécriture doublée par une autre :
	// l'étape ou le jalon a changé entre la lecture et l'écriture — deux
	// personnes qui replanifient en même temps. Le perdant relit et recommence ;
	// écraser en silence ferait régresser l'état posé par le gagnant.
	ErrConcurrentUpdate = errors.New("planning : l'élément a été modifié entre-temps")
)
