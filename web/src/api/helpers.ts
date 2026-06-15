import type { Container } from './types'

/** Whether the container is in the running state. */
export function isRunning(c: Container): boolean {
  return c.status.state === 'running'
}

/**
 * The container's dedicated IPv4 address with the CIDR suffix stripped, or ""
 * when the container is not running (stopped containers carry no runtime
 * network assignment, so `status.networks` is empty). Callers render "—" for
 * the empty case — never an IP for a stopped container.
 */
export function primaryIPv4(c: Container): string {
  const nets = c.status.networks
  if (!nets || nets.length === 0) return ''
  const addr = nets[0].ipv4Address ?? ''
  const slash = addr.indexOf('/')
  return slash >= 0 ? addr.slice(0, slash) : addr
}
