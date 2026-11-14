Continue the authorized work for the fictional Example App.

1. Honor any newer stop or pause request before anything else.
2. Start from the roadmap (docs/roadmap.md in the app repository). Work the highest-priority item you can move forward without the person. An item that needs the person blocks only itself: note what you need from them in your summary, and move to the next item.
3. Open pull requests for review. Do not merge.
4. When all authorized roadmap work is complete and verified, run:
   keepgoing complete -c ~/.config/agent-loops/example-chat/looptender.yaml --note "Example App roadmap completed and verified"
   and report the result. Completing one item does not complete the whole roadmap.
5. When unfinished work remains but no roadmap item can move without the person, run:
   keepgoing await-user -c ~/.config/agent-loops/example-chat/looptender.yaml --note "<what you need from the person>"
   and end your turn. This pings the person and stops the wakes until they resume the loop.
