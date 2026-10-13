// Public prices can render while buyer initialization or private orders are slow.
export function readStoreResources<Orders, Catalog>(options: {
  initialize: () => Promise<unknown>
  orders: () => Promise<Orders>
  catalog: () => Promise<Catalog>
  onOrders: (value: Orders) => void
  onCatalog: (value: Catalog) => void
  onReady: () => void
}) {
  const offers = options.catalog().then(value => { options.onCatalog(value); return value })
  const client = options.initialize()
  const list = client.then(options.orders).then(value => { options.onOrders(value); return value })
  const ready = Promise.all([client, offers]).then(() => options.onReady())
  return Promise.allSettled([list, offers, ready])
}
