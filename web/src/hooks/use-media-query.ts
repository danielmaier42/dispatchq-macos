import {useSyncExternalStore} from 'react'

// useMediaQuery is whether the window currently matches a CSS media query,
// updated as the window is resized.
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const list = window.matchMedia(query)
      list.addEventListener('change', onChange)
      return () => list.removeEventListener('change', onChange)
    },
    () => window.matchMedia(query).matches,
  )
}
