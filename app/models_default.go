//go:build !e2e

package main

import "penguin.local/app/internal/models"

func modelConfig() models.Config {
	return models.Config{}
}
