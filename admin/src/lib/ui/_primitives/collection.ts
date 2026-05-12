import { useCallback, useRef } from 'preact/hooks'

export function useCollection<T = HTMLElement>() {
  const items = useRef<Set<T>>(new Set())

  const register = useCallback((el: T | null) => {
    if (!el) return
    items.current.add(el)
    return () => {
      items.current.delete(el)
    }
  }, [])

  const getItems = useCallback((): T[] => {
    const arr = Array.from(items.current)
    if (arr.length && arr[0] instanceof Element) {
      arr.sort((a, b) => {
        const rel = (a as unknown as Node).compareDocumentPosition(b as unknown as Node)
        if (rel & Node.DOCUMENT_POSITION_FOLLOWING) return -1
        if (rel & Node.DOCUMENT_POSITION_PRECEDING) return 1
        return 0
      })
    }
    return arr
  }, [])

  return { register, getItems }
}
