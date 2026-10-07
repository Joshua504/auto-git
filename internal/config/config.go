package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Interval     string       `yaml:"interval"`
	Repositories []Repository `yaml:"repositories"`
	Commit       CommitConfig `yaml:"commit"`
}

type Repository struct {
	Path string `yaml:"path"`
}

type CommitConfig struct {
	Message string `yaml:"message"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return Config{}, err
	}

	return config, err
}

func (c Config) Validate() error {
	if c.Interval == "" {
		return fmt.Errorf("interval is required")
	}
	if len(c.Repositories) == 0 {
		return fmt.Errorf("at least one repository is required")
	}
	for _, repository := range c.Repositories {
		if repository.Path == "" {
			return fmt.Errorf("repository path is required")
		}
	}

	if c.Commit.Message == "" {
		return fmt.Errorf("commit message is required")
	}

	return nil
}
