# Avoid potential confusion about what shell to use
SHELL := /bin/bash

# Delete implicit rules not used here (clutters debug output).
MAKEFLAGS += --no-builtin-rules

# Turn off .INTERMEDIATE file removal by marking all files as
# .SECONDARY.  .INTERMEDIATE file removal is a space-saving hack from
# a time when drives were small; on modern computers with plenty of
# storage, it causes nothing but headaches.
#
# https://news.ycombinator.com/item?id=16486331
.SECONDARY:

# If a recipe errors, remove the target it was building.  This
# prevents outdated/incomplete results of failed runs from tainting
# future runs.  The only reason .DELETE_ON_ERROR is off by default is
# for historical compatibility.
#
# If for some reason this behavior is not desired for a specific
# target, mark that target as .PRECIOUS.
.DELETE_ON_ERROR:

PLUGIN_REGISTRY ?= datawire
PLUGIN_NAME = telemount
PLUGIN_TAG ?= latest
PLUGIN_FQN = $(PLUGIN_REGISTRY)/$(PLUGIN_NAME)
PLUGIN_IMAGE = $(PLUGIN_FQN):$(PLUGIN_TAG)

BUILD_DIR=build-output

export DOCKER_BUILDKIT := 1

clean:
	rm -rf $(BUILD_DIR)

rootfs:
	docker build -q -t $(PLUGIN_FQN):rootfs .
	mkdir -p $(BUILD_DIR)/rootfs
	docker create --name tmp $(PLUGIN_FQN):rootfs
	docker export tmp | tar -x -C $(BUILD_DIR)/rootfs
	cp config.json $(BUILD_DIR)
	docker rm -vf tmp

create: rootfs
	docker plugin rm -f $(PLUGIN_IMAGE) 2> /dev/null || true
	docker plugin create $(PLUGIN_IMAGE) $(BUILD_DIR)

enable: create
	docker plugin enable $(PLUGIN_IMAGE)

push:  clean enable
	docker plugin push $(PLUGIN_IMAGE)

debug: push
	docker plugin rm -f $(PLUGIN_IMAGE) 2> /dev/null || true
	docker plugin install $(PLUGIN_IMAGE) DEBUG=true