# Put Port On Desktop (把端口放到桌面) Makefile

DOCKER_COMPOSE ?= docker compose

.PHONY: all build up down rebuild status logs clean

all: build

build:
	$(DOCKER_COMPOSE) build

up:
	$(DOCKER_COMPOSE) up -d

down:
	$(DOCKER_COMPOSE) down

rebuild:
	$(DOCKER_COMPOSE) up -d --build

status:
	$(DOCKER_COMPOSE) ps

logs:
	$(DOCKER_COMPOSE) logs -f --tail=100

clean:
	$(DOCKER_COMPOSE) down -v --remove-orphans
