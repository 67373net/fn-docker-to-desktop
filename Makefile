# 把Docker放到桌面 (fn-docker-to-desktop) Makefile

.PHONY: all fpk build clean run test

# 默认构建飞牛OS .fpk 安装包
all: fpk

# 构建飞牛OS 原生 .fpk 安装包
fpk:
	./scripts/build-fpk.sh

# 编译本地可执行二进制
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o fn-docker-to-desktop ./cmd/server

# 本地直接运行测试
run: build
	./fn-docker-to-desktop -port 5900

clean:
	rm -f *.fpk
	rm -f fn-docker-to-desktop
	rm -f fnos-app/app.tgz
	rm -f fnos-app/app/fn-docker-to-desktop

