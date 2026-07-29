import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import {
  listOrgMembers,
  addOrgMember,
  removeOrgMember,
  getProfile,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { OrgMember } from '@/gen/gritz/v1/gritz_pb'
import { useOrgId } from '@/hooks/use-org-id'
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
import { Input } from '@/components/ui/input'
import { RelativeTime } from '@/components/relative-time'
import { Trash2, Loader2, UserPlus } from 'lucide-react'
import { useState } from 'react'

export const Route = createFileRoute('/members/')({
  component: MembersPage,
})

function MembersPage() {
  const { data: profileData } = useQuery(getProfile, {})
  const { data, isLoading, error, refetch } = useQuery(
    listOrgMembers,
    {},
    {
      refetchInterval: 6000,
    },
  )

  const orgId = useOrgId()
  const isOwner =
    profileData?.orgs.some(
      (org) => String(org.id) === orgId && org.owner === profileData.profile?.id,
    ) ?? false

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-muted-foreground">Loading members...</div>
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

  const members = data?.members ?? []

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Members</h1>
        {isOwner && <AddMemberForm onAdd={refetch} />}
      </div>
      {members.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No members found</div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Email</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Added</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {members.map((member) => (
              <MemberRow key={member.userId} member={member} onRemove={refetch} isOwner={isOwner} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

function AddMemberForm({ onAdd }: { onAdd: () => void }) {
  const [email, setEmail] = useState('')
  const mutation = useMutation(addOrgMember, {
    onSuccess: () => {
      setEmail('')
      onAdd()
    },
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email.trim()) return
    await mutation.mutateAsync({ email: email.trim() })
  }

  return (
    <form onSubmit={handleSubmit} className="flex gap-2">
      <Input
        type="email"
        placeholder="Email address"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="max-w-sm"
      />
      <Button type="submit" disabled={mutation.isPending || !email.trim()}>
        {mutation.isPending ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : (
          <UserPlus className="h-4 w-4" />
        )}
        Add Member
      </Button>
      {mutation.error && (
        <span className="text-destructive text-sm self-center">{mutation.error.message}</span>
      )}
    </form>
  )
}

function MemberRow({
  member,
  onRemove,
  isOwner,
}: {
  member: OrgMember
  onRemove: () => void
  isOwner: boolean
}) {
  const removeMutation = useMutation(removeOrgMember, {
    onSuccess: () => onRemove(),
  })

  const handleRemove = async () => {
    await removeMutation.mutateAsync({ userId: member.userId })
  }

  return (
    <TableRow>
      <TableCell className="font-medium">{member.name || '-'}</TableCell>
      <TableCell className="text-muted-foreground">{member.email}</TableCell>
      <TableCell className="text-muted-foreground">{member.role}</TableCell>
      <TableCell className="text-muted-foreground">
        {member.createdAt ? <RelativeTime date={timestampDate(member.createdAt)} /> : '-'}
      </TableCell>
      <TableCell>
        {isOwner && (
          <Button
            variant="destructive"
            size="sm"
            onClick={handleRemove}
            disabled={removeMutation.isPending || member.role === 'owner'}
          >
            {removeMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Trash2 className="h-4 w-4" />
            )}
            Remove
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}
