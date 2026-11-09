Continue the authorized work for the fictional Example App.

1. Honor any newer stop or pause request before anything else.
2. Start from the roadmap (docs/roadmap.md in the app repository). Work the highest-priority item you can move forward without the person. An item that needs the person blocks only itself: note what you need from them in your summary, and move to the next item.
3. Open pull requests for review. Do not merge.
4. Only when no roadmap item at all can move without the person, run:
   keepgoing await-user -c ~/.config/keepgoing/example-chat/keepgoing.yaml --note "<what you need from the person>"
   and end your turn. This pings the person and stops the wakes until they resume the loop.
