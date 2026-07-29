import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { listKeys, deleteKey } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Key } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { Plus, Trash2, Loader2 } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/keys/')({
  component: KeysPage,
})

function KeysPage() {
  const orgId = useOrgId()
  const { data, isLoading, error, refetch } = useQuery(
    listKeys,
    {},
    {
      refetchInterval: 6000,
    },
  )

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-muted-foreground">Loading API keys...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const keys = data?.keys ?? []

  return (
    <div className="container mx-auto py-8 px-4">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Keys</h1>
        <Link to="/keys/new" search={{ org: orgId }}>
          <Button>
            <Plus className="h-4 w-4" />
            API Key
          </Button>
        </Link>
      </div>
      {keys.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No API keys found</div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Created</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((key) => (
              <KeyRow key={key.id} apiKey={key} onDelete={refetch} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

function KeyRow({ apiKey, onDelete }: { apiKey: Key; onDelete: () => void }) {
  const deleteMutation = useMutation(deleteKey, {
    onSuccess: () => onDelete(),
  })

  const handleDelete = async () => {
    await deleteMutation.mutateAsync({ id: apiKey.id })
  }

  const isExpired = apiKey.expiresAt && timestampDate(apiKey.expiresAt) < new Date()

  return (
    <TableRow>
      <TableCell className="font-medium">{apiKey.name}</TableCell>
      <TableCell className="text-muted-foreground">
        {apiKey.expiresAt ? (
          <span className={isExpired ? 'text-destructive' : ''}>
            {isExpired ? 'Expired ' : ''}
            <RelativeTime date={timestampDate(apiKey.expiresAt)} />
          </span>
        ) : (
          'Never'
        )}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {apiKey.createdAt ? <RelativeTime date={timestampDate(apiKey.createdAt)} /> : '-'}
      </TableCell>
      <TableCell>
        <Button
          variant="destructive"
          size="sm"
          onClick={handleDelete}
          disabled={deleteMutation.isPending}
        >
          {deleteMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Trash2 className="h-4 w-4" />
          )}
          Delete
        </Button>
      </TableCell>
    </TableRow>
  )
}
