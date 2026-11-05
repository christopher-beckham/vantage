#!/bin/sh
# Written by Vantage, and rewritten whenever it starts: do not edit.
#
# Waits until a review batch is ready in .vantage/outbox, then exits, which
# wakes the agent that ran it in the background. A ready batch is a name ending
# in ".md"; every other state adds a suffix after it. The loop lives in this
# file so the agent runs one plain command that a permission rule can allow,
# and so it runs under sh whatever the user's own shell is: under zsh, a glob
# that matches nothing is an error printed every second.
cd "$(dirname "$0")/.." || exit 1
until ls .vantage/outbox 2>/dev/null | grep -q '\.md$'; do
	sleep 1
done
