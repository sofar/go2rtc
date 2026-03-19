PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
CONFDIR ?= /etc/go2rtc
DATADIR ?= /var/lib/go2rtc
UNITDIR ?= /usr/lib/systemd/system

BINARY = go2rtc
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install uninstall enable start stop restart status clean test

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

test:
	go test ./...

install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	install -Dm644 contrib/go2rtc.service $(DESTDIR)$(UNITDIR)/go2rtc.service
	install -dm755 $(DESTDIR)$(CONFDIR)
	install -dm755 $(DESTDIR)$(DATADIR)
	install -dm755 $(DESTDIR)$(DATADIR)/recordings
	@if [ ! -f $(DESTDIR)$(CONFDIR)/go2rtc.yaml ]; then \
		install -Dm644 contrib/go2rtc.yaml.example $(DESTDIR)$(CONFDIR)/go2rtc.yaml; \
		echo "Installed default config to $(CONFDIR)/go2rtc.yaml"; \
	else \
		echo "Config exists at $(CONFDIR)/go2rtc.yaml — not overwriting"; \
	fi
	@echo ""
	@echo "Installed. Next steps:"
	@echo "  1. Edit $(CONFDIR)/go2rtc.yaml"
	@echo "  2. sudo systemctl daemon-reload"
	@echo "  3. sudo systemctl enable --now go2rtc"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BINARY)
	rm -f $(DESTDIR)$(UNITDIR)/go2rtc.service
	@echo "Removed binary and service file."
	@echo "Config and data kept at $(CONFDIR) and $(DATADIR)"

enable:
	systemctl daemon-reload
	systemctl enable go2rtc

start:
	systemctl start go2rtc

stop:
	systemctl stop go2rtc

restart:
	systemctl restart go2rtc

status:
	systemctl status go2rtc

clean:
	rm -f $(BINARY)
