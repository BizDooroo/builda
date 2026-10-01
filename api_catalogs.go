package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var errCatalogNotFound = errors.New("catalog not found")

// catalogUsage reports which job parameters read from a catalog so the UI can
// show that one new project entry updates every applicable job at once.
type catalogUsage struct {
	JobID     string   `json:"job_id"`
	JobName   string   `json:"job_name"`
	Parameter string   `json:"parameter"`
	Labels    []string `json:"labels,omitempty"`
	Matches   int      `json:"matches"`
}

type catalogView struct {
	CatalogConfig
	UsedBy []catalogUsage `json:"used_by"`
}

func buildCatalogView(cfg ControllerConfig, catalog CatalogConfig) catalogView {
	usage := make([]catalogUsage, 0)
	for _, job := range cfg.Jobs {
		for _, param := range job.Parameters {
			if param.Catalog != catalog.ID {
				continue
			}
			usage = append(usage, catalogUsage{
				JobID:     job.ID,
				JobName:   job.Name,
				Parameter: param.ID,
				Labels:    param.CatalogLabels,
				Matches:   len(filterCatalogOptions(catalog.Options, param.CatalogLabels)),
			})
		}
	}
	return catalogView{CatalogConfig: catalog, UsedBy: usage}
}

func (s *ControllerAPI) handleListCatalogs(w http.ResponseWriter, r *http.Request, who principal) {
	cfg := s.controller.Config()
	views := make([]catalogView, 0, len(cfg.Catalogs))
	for _, catalog := range cfg.Catalogs {
		views = append(views, buildCatalogView(cfg, catalog))
	}
	respondJSON(w, map[string]any{"catalogs": views})
}

func (s *ControllerAPI) handleGetCatalog(w http.ResponseWriter, r *http.Request, who principal) {
	cfg := s.controller.Config()
	catalog, ok := findCatalog(cfg, r.PathValue("id"))
	if !ok {
		respondError(w, http.StatusNotFound, "catalog not found")
		return
	}
	respondJSON(w, buildCatalogView(cfg, catalog))
}

func (s *ControllerAPI) handleCreateCatalog(w http.ResponseWriter, r *http.Request, who principal) {
	var catalog CatalogConfig
	if err := decodeJSONBody(r, &catalog, 512<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid catalog document: "+err.Error())
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		if _, exists := findCatalog(*cfg, strings.TrimSpace(catalog.ID)); exists {
			return fmt.Errorf("catalog %q already exists", catalog.ID)
		}
		cfg.Catalogs = append(cfg.Catalogs, catalog)
		return nil
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": catalog.ID})
}

func (s *ControllerAPI) handleUpdateCatalog(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	var catalog CatalogConfig
	if err := decodeJSONBody(r, &catalog, 512<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid catalog document: "+err.Error())
		return
	}
	if strings.TrimSpace(catalog.ID) == "" {
		catalog.ID = id
	}
	if catalog.ID != id {
		respondError(w, http.StatusBadRequest, "catalog id cannot be changed")
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Catalogs {
			if cfg.Catalogs[index].ID == id {
				cfg.Catalogs[index] = catalog
				return nil
			}
		}
		return errCatalogNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *ControllerAPI) handleDeleteCatalog(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for _, job := range cfg.Jobs {
			for _, param := range job.Parameters {
				if param.Catalog == id {
					return fmt.Errorf("catalog %q is used by job %q parameter %q", id, job.ID, param.ID)
				}
			}
		}
		for index := range cfg.Catalogs {
			if cfg.Catalogs[index].ID == id {
				cfg.Catalogs = append(cfg.Catalogs[:index], cfg.Catalogs[index+1:]...)
				return nil
			}
		}
		return errCatalogNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}
