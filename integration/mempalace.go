package integration

import (
	"log"
	"os/exec"
)

func InstallMempalace() error {
	log.Println("Checking/Installing mempalace via npm...")
	cmd := exec.Command("npm", "install", "-g", "https://github.com/milla-jovovich/mempalace")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to install mempalace: %v\nOutput: %s", err, string(output))
		return err
	}
	log.Println("Mempalace installed successfully.")
	return nil
}
