# Put Port On Desktop (把端口放到桌面) Makefile

DOCKER_COMPOSE ?= docker compose

.PHONY: all fpk build up down rebuild status logs clean

# 默认构建飞牛OS .fpk 安装包
all: fpk

# 构建飞牛OS 原生 .fpk 安装包
fpk:
	./scripts/build-fpk.sh

# 编译 Docker 镜像
build:
	$(DOCKER_COMPOSE) build

# 启动 Docker 容器 (可选/调试)
up:
	$(DOCKER_COMPOSE) up -d

# 停止 Docker 容器
down:
	$(DOCKER_COMPOSE) down

# 重新构建并启动 Docker 容器
rebuild:
	$(DOCKER_COMPOSE) up -d --build

# 查看 Docker 容器状态
status:
	$(DOCKER_COMPOSE) ps

# 查看 Docker 容器日志
logs:
	$(DOCKER_COMPOSE) logs -f --tail=100

clean:
	rm -f *.fpk
	rm -f fnos-app/app/put-port-on-desktop
	$(DOCKER_COMPOSE) down -v --remove-orphans
