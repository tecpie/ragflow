/*
 *  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

import { SelectWithSearch } from '@/components/originui/select-with-search';
import { RAGFlowFormItem } from '@/components/ragflow-form';
import { useFetchOntologies } from '@/hooks/use-ontology-request';
import { useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';

type OntologyIdFieldProps = {
  index: number;
};

export function OntologyIdField({ index }: OntologyIdFieldProps) {
  const { t } = useTranslation();
  const { ontologies, loading, isError } = useFetchOntologies();

  const options = useMemo(
    () =>
      ontologies.map((item) => ({
        label: item.name,
        value: item.id,
        description: item.base_iri,
        keywords: [item.name, item.base_iri].filter(
          (keyword) => keyword !== '',
        ),
      })),
    [ontologies],
  );

  const renderMissingOntology = useCallback(
    () => t('knowledgeCompilation.ontologyMissing'),
    [t],
  );

  return (
    <RAGFlowFormItem
      name={`templates.${index}.config.ontology_id`}
      label={t('knowledgeCompilation.ontologyId')}
      tooltip={t('knowledgeCompilation.ontologyIdTip')}
    >
      {(field) => (
        <SelectWithSearch
          value={typeof field.value === 'string' ? field.value : ''}
          onChange={field.onChange}
          disabled={field.disabled}
          allowClear
          loading={loading}
          options={options}
          placeholder={t('knowledgeCompilation.ontologyIdPlaceholder')}
          renderMissingValue={isError ? undefined : renderMissingOntology}
        />
      )}
    </RAGFlowFormItem>
  );
}
