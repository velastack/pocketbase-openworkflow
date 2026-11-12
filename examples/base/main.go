package main

import (
	"log"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"github.com/velastack/pocketbase-openworkflow"
)

func main() {
	app := pocketbase.New()

	openworkflow.MustRegister(app, openworkflow.Config{})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
