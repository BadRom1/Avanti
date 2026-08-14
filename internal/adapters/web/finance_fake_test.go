package web_test

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// memFinanceRepo est un [finance.Repository] en mémoire pour les tests de
// l'adapter web.
//
// Il tient les mêmes promesses que le dépôt PostgreSQL — erreurs typées,
// réécriture qui échoue sur une pièce inconnue, invariant du cumul revérifié
// sous verrou dans CreateAcompte — parce que les gestionnaires HTTP distinguent
// ces cas et que les vérifier contre un fake plus permissif ne prouverait rien.
type memFinanceRepo struct {
	mu           sync.Mutex
	factures     map[finance.ID]finance.Facture
	factureOrder []finance.ID
	acomptes     map[finance.ID]finance.Acompte
	acompteOrder []finance.ID
}

func newMemFinanceRepo() *memFinanceRepo {
	return &memFinanceRepo{
		factures: make(map[finance.ID]finance.Facture),
		acomptes: make(map[finance.ID]finance.Acompte),
	}
}

func (r *memFinanceRepo) CreateFacture(_ context.Context, facture finance.Facture) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.factures[facture.ID] = facture
	r.factureOrder = append(r.factureOrder, facture.ID)

	return nil
}

func (r *memFinanceRepo) FactureByID(_ context.Context, id finance.ID) (finance.Facture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	facture, ok := r.factures[id]
	if !ok {
		return finance.Facture{}, finance.ErrUnknownFacture
	}

	return facture, nil
}

func (r *memFinanceRepo) ListFactures(_ context.Context) ([]finance.Facture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	factures := make([]finance.Facture, 0, len(r.factureOrder))
	for _, id := range r.factureOrder {
		factures = append(factures, r.factures[id])
	}

	return factures, nil
}

// UpdateFacture honore la garde optimiste du contrat de port.
func (r *memFinanceRepo) UpdateFacture(_ context.Context, facture finance.Facture, expected time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.factures[facture.ID]
	if !ok {
		return finance.ErrUnknownFacture
	}
	if !current.UpdatedAt.Equal(expected) {
		return finance.ErrConcurrentUpdate
	}
	r.factures[facture.ID] = facture

	return nil
}

func (r *memFinanceRepo) CreateAcompte(_ context.Context, acompte finance.Acompte, montantEngage finance.Montant) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if acompte.DevisID != "" {
		var cumul finance.Montant
		for _, id := range r.acompteOrder {
			if r.acomptes[id].DevisID == acompte.DevisID {
				cumul += r.acomptes[id].Montant
			}
		}
		if cumul+acompte.Montant > montantEngage {
			return fmt.Errorf("%w : %s", finance.ErrAcomptesExceedEngagement, acompte.DevisID)
		}
	}

	r.acomptes[acompte.ID] = acompte
	r.acompteOrder = append(r.acompteOrder, acompte.ID)

	return nil
}

func (r *memFinanceRepo) AcompteByID(_ context.Context, id finance.ID) (finance.Acompte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acompte, ok := r.acomptes[id]
	if !ok {
		return finance.Acompte{}, finance.ErrUnknownAcompte
	}

	return acompte, nil
}

func (r *memFinanceRepo) ListAcomptes(_ context.Context) ([]finance.Acompte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acomptes := make([]finance.Acompte, 0, len(r.acompteOrder))
	for _, id := range r.acompteOrder {
		acomptes = append(acomptes, r.acomptes[id])
	}

	return acomptes, nil
}

// UpdateAcompte honore la même garde optimiste que UpdateFacture.
func (r *memFinanceRepo) UpdateAcompte(_ context.Context, acompte finance.Acompte, expected time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.acomptes[acompte.ID]
	if !ok {
		return finance.ErrUnknownAcompte
	}
	if !current.UpdatedAt.Equal(expected) {
		return finance.ErrConcurrentUpdate
	}
	r.acomptes[acompte.ID] = acompte

	return nil
}

func (r *memFinanceRepo) SumAcomptesByDevis(_ context.Context, devisID string) (finance.Montant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var cumul finance.Montant
	for _, id := range r.acompteOrder {
		if r.acomptes[id].DevisID == devisID {
			cumul += r.acomptes[id].Montant
		}
	}

	return cumul, nil
}

// factureParEntreprise retrouve une facture par le nom de qui l'a émise, pour
// que les tests désignent « Charpentes du Val » plutôt qu'un UUID.
func (r *memFinanceRepo) factureParEntreprise(entreprise string) (finance.Facture, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.factureOrder {
		if r.factures[id].Entreprise == entreprise {
			return r.factures[id], true
		}
	}

	return finance.Facture{}, false
}

// acompteParEntreprise retrouve un acompte par le nom de qui l'a reçu.
func (r *memFinanceRepo) acompteParEntreprise(entreprise string) (finance.Acompte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.acompteOrder {
		if r.acomptes[id].Entreprise == entreprise {
			return r.acomptes[id], true
		}
	}

	return finance.Acompte{}, false
}

// Les formats d'export des tests web. Les vrais formats vivent dans
// adapters/export, qu'un test de cette famille n'a pas le droit d'importer
// (R4 : une famille d'adapters n'en importe pas une autre) ; leur rendu a ses
// propres tests dans leur package. Ces implémentations-ci écrivent le CONTENU
// du dossier — c'est l'assemblage fait par l'adapter web qui est sous test :
// le vrai branchement bout à bout se joue dans cmd/avanti, seul endroit
// autorisé à connaître les deux familles.

// csvExportStub écrit un vrai CSV, une ligne par pièce du dossier.
type csvExportStub struct{}

func (csvExportStub) ContentType() string   { return "text/csv; charset=utf-8" }
func (csvExportStub) FileExtension() string { return "csv" }

func (csvExportStub) Write(w io.Writer, dossier finance.DossierAssurance) error {
	writer := csv.NewWriter(w)

	rows := make([][]string, 0, 2+len(dossier.Factures)+len(dossier.Acomptes))
	rows = append(rows, []string{"Type", "Devis", "Entreprise", "Montant", "Pièces"})
	for _, ligne := range dossier.Factures {
		pieces := ""
		for _, piece := range ligne.Pieces {
			pieces += piece.FileName + ";"
		}
		rows = append(rows, []string{"Facture", ligne.DevisLibelle, ligne.Entreprise, ligne.Montant.String(), pieces})
	}
	for _, ligne := range dossier.Acomptes {
		rows = append(rows, []string{"Acompte", ligne.DevisLibelle, ligne.Entreprise, ligne.Montant.String(), ""})
	}
	rows = append(rows, []string{"Totaux", "", "", dossier.Totaux.Engage.String(), ""})

	return writer.WriteAll(rows)
}

// pdfExportStub écrit un document qui commence par l'entête %PDF-, suivi des
// montants du dossier en clair.
type pdfExportStub struct{}

func (pdfExportStub) ContentType() string   { return "application/pdf" }
func (pdfExportStub) FileExtension() string { return "pdf" }

func (pdfExportStub) Write(w io.Writer, dossier finance.DossierAssurance) error {
	if _, err := io.WriteString(w, "%PDF-1.4 factice\n"); err != nil {
		return err
	}
	for _, ligne := range dossier.Factures {
		if _, err := fmt.Fprintf(w, "%s %s\n", ligne.Entreprise, ligne.Montant); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "total %s\n", dossier.Totaux.Paye)
	return err
}
